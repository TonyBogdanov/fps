package main

import (
	"errors"
	"fmt"
	"gopkg.in/Knetic/govaluate.v2"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const TargetBlockSize = 448
const MemoryLimit = 4

var inputPath string
var projectDir string
var meta *Metadata

type Metadata struct {
	Width       int
	Height      int
	Frames      int
	FPS         int
	BlockWidth  int
	BlockHeight int
}

func die(err error) {
	if nil != err {
		log.Println(err)
		log.Println("--- ERROR ---")

		time.Sleep(5 * time.Minute)
		os.Exit(1)
	}
}

func pad(value, length int) string {
	return fmt.Sprintf("%0*d", length, value)
}

func eval(value string) int {
	expr, err := govaluate.NewEvaluableExpression(value)
	die(err)

	result, err := expr.Evaluate(map[string]interface{}{})
	die(err)

	return int(math.Round(result.(float64)))
}

func probe(file, stream string) string {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-select_streams", "v",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-show_entries", stream,
		file,
	)

	out, err := cmd.Output()
	die(err)

	return strings.Trim(string(out), "\r\n")
}

func exists(file string) bool {
	_, err := os.Stat(file)
	if nil == err {
		return true
	}

	if !errors.Is(err, os.ErrNotExist) {
		die(err)
	}

	return false
}

func computeBlockSize(size int) int {
	for i := 1; i <= size; i++ {
		if 0 == size%i && TargetBlockSize >= size/i {
			return i
		}
	}

	return size
}

func run(setup func(queue func(job func()))) {
	parallelism := runtime.NumCPU()
	mx := sync.Mutex{}
	running := 0

	queue := func(job func()) {
		for {
			mx.Lock()
			if running >= parallelism {
				mx.Unlock()
				time.Sleep(time.Millisecond)

				continue
			}

			running++
			mx.Unlock()

			go func() {
				job()

				mx.Lock()
				running--
				mx.Unlock()
			}()

			return
		}
	}

	setup(queue)

	for {
		mx.Lock()
		if 0 == running {
			mx.Unlock()
			break
		}

		time.Sleep(time.Millisecond)
		mx.Unlock()
	}
}

func decodeImage(file string) image.Image {
	reader, err := os.Open(file)
	die(err)

	img, err := png.Decode(reader)
	die(err)

	die(reader.Close())
	return img
}

func encodeImage(img image.Image, file string) {
	writer, err := os.Create(file)
	die(err)

	die(png.Encode(writer, img))
	die(writer.Close())
}

func initialize() {
	log.Printf("Initializing project.\n")
	if len(os.Args) < 2 {
		die(fmt.Errorf("no input provided, drop a video file onto this executable"))
	}

	input, err := filepath.Abs(os.Args[1])
	die(err)

	executable, err := os.Executable()
	die(err)

	inputPath = input
	projectDir = filepath.Join(
		filepath.Dir(executable),
		"data",
		filepath.Base(input[:len(input)-len(filepath.Ext(input))]),
	)

	die(os.MkdirAll(projectDir, os.ModePerm))

	log.Printf("Analyzing video file: %s.\n", inputPath)
	meta = &Metadata{
		Frames: eval(probe(inputPath, "stream=nb_frames")),
		FPS:    eval(probe(inputPath, "stream=avg_frame_rate")),
	}

	rotation := probe(inputPath, "stream_side_data=rotation")
	if "" == rotation || "-180" == rotation || "180" == rotation {
		meta.Width = eval(probe(inputPath, "stream=width"))
		meta.Height = eval(probe(inputPath, "stream=height"))
	} else if "-90" == rotation || "90" == rotation || "-270" == rotation || "270" == rotation {
		meta.Width = eval(probe(inputPath, "stream=height"))
		meta.Height = eval(probe(inputPath, "stream=width"))
	} else {
		die(fmt.Errorf("Unknown rotation value: %s.\n", rotation))
	}

	meta.BlockWidth = computeBlockSize(meta.Width * 5)
	meta.BlockHeight = computeBlockSize(meta.Height)
}

func unpackVideo() {
	log.Printf("Unpacking video frames from: %s.\n", inputPath)
	cmd := exec.Command(
		"ffmpeg",
		"-i", inputPath,
		"-pix_fmt", "rgb48be",
		"-y", filepath.Join(projectDir, "p-%08d.png"),
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	die(cmd.Run())
}

func unpackFrames() {
	run(func(queue func(job func())) {
		for i := 1; i <= meta.Frames; i++ {
			queue(func(i int) func() {
				return func() {
					log.Printf("Unpacking frame %d of %d [%.2f%%].\n", i, meta.Frames,
						100*float64(i)/float64(meta.Frames))

					packedPath := filepath.Join(projectDir, "p-"+pad(i, 8)+".png")
					unpackedPath := filepath.Join(projectDir, "u-"+pad(i, 8)+".png")

					packed := decodeImage(packedPath)
					unpacked := image.NewRGBA(image.Rect(0, 0, meta.Width*5, meta.Height))

					for x := 0; x < meta.Width; x++ {
						for y := 0; y < meta.Height; y++ {
							c := packed.(*image.RGBA64).RGBA64At(x, y)

							unpacked.Set(x, y, color.RGBA{
								R: uint8((c.R >> 8) & 0b11111111),
								G: uint8((c.G >> 8) & 0b11111111),
								B: uint8((c.B >> 8) & 0b11111111),
								A: 0xFF,
							})

							unpacked.Set(x+meta.Width, y, color.RGBA{
								R: uint8((c.R>>8)&0b11111100) | uint8(c.R&0b11),
								G: uint8((c.G>>8)&0b11111100) | uint8(c.G&0b11),
								B: uint8((c.B>>8)&0b11111100) | uint8(c.B&0b11),
								A: 0xFF,
							})

							unpacked.Set(x+meta.Width*2, y, color.RGBA{
								R: uint8((c.R>>8)&0b11111100) | uint8((c.R>>2)&0b11),
								G: uint8((c.G>>8)&0b11111100) | uint8((c.G>>2)&0b11),
								B: uint8((c.B>>8)&0b11111100) | uint8((c.B>>2)&0b11),
								A: 0xFF,
							})

							unpacked.Set(x+meta.Width*3, y, color.RGBA{
								R: uint8((c.R>>8)&0b11111100) | uint8((c.R>>4)&0b11),
								G: uint8((c.G>>8)&0b11111100) | uint8((c.G>>4)&0b11),
								B: uint8((c.B>>8)&0b11111100) | uint8((c.B>>4)&0b11),
								A: 0xFF,
							})

							unpacked.Set(x+meta.Width*4, y, color.RGBA{
								R: uint8((c.R>>8)&0b11111100) | uint8((c.R>>6)&0b11),
								G: uint8((c.G>>8)&0b11111100) | uint8((c.G>>6)&0b11),
								B: uint8((c.B>>8)&0b11111100) | uint8((c.B>>6)&0b11),
								A: 0xFF,
							})
						}
					}

					encodeImage(unpacked, unpackedPath)
					die(os.Remove(packedPath))
				}
			}(i))
		}
	})
}

func interpolateFrames() {
	log.Printf(
		"Interpolating %d video frames with block size %dx%d.\n",
		meta.Frames,
		5*meta.Width/meta.BlockWidth,
		meta.Height/meta.BlockHeight,
	)

	cmd := exec.Command(
		"docker", "run",
		"--rm",
		"--network", "host",
		"--gpus", "all",
		"--memory", fmt.Sprintf("%dg", MemoryLimit),
		"-v", fmt.Sprintf("%s:/data", projectDir),
		"tonybogdanov/fps:latest",
		"python",
		"-m", "eval.interpolator_cli",
		"--pattern", "/data",
		"--model_path", "/models/pretrained_models/film_net/Style/saved_model",
		"--block_width", strconv.Itoa(meta.BlockWidth),
		"--block_height", strconv.Itoa(meta.BlockHeight),
		"--times_to_interpolate", "1",
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	die(cmd.Run())

	for i := 1; i <= meta.Frames; i++ {
		log.Printf("Cleaning up frame %d of %d [%.2f%%].\n", i, meta.Frames, 100*float64(i)/float64(meta.Frames))
		die(os.Remove(filepath.Join(projectDir, "u-"+pad(i, 8)+".png")))
	}
}

func packFrames() {
	run(func(queue func(job func())) {
		for i := 1; i <= (meta.Frames-1)*2+1; i++ {
			queue(func(i int) func() {
				return func() {
					log.Printf("Packing frame %d of %d [%.2f%%].\n", i, (meta.Frames-1)*2+1,
						100*float64(i)/float64((meta.Frames-1)*2+1))

					unpackedPath := filepath.Join(projectDir, "f-"+pad(i-1, 8)+".png")
					packedPath := filepath.Join(projectDir, "p-"+pad(i, 8)+".png")

					unpacked := decodeImage(unpackedPath)
					packed := image.NewRGBA64(image.Rect(0, 0, meta.Width, meta.Height))

					for x := 0; x < meta.Width; x++ {
						for y := 0; y < meta.Height; y++ {
							c0 := unpacked.(*image.RGBA).RGBAAt(x, y)
							c1 := unpacked.(*image.RGBA).RGBAAt(x+meta.Width, y)
							c2 := unpacked.(*image.RGBA).RGBAAt(x+2*meta.Width, y)
							c3 := unpacked.(*image.RGBA).RGBAAt(x+3*meta.Width, y)
							c4 := unpacked.(*image.RGBA).RGBAAt(x+4*meta.Width, y)

							packed.Set(x, y, color.RGBA64{
								R: (uint16(c0.R) << 8) | (uint16(c1.R) & 0b11) | ((uint16(c2.R) & 0b11) << 2) |
									((uint16(c3.R) & 0b11) << 4) | ((uint16(c4.R) & 0b11) << 6),
								G: (uint16(c0.G) << 8) | (uint16(c1.G) & 0b11) | ((uint16(c2.G) & 0b11) << 2) |
									((uint16(c3.G) & 0b11) << 4) | ((uint16(c4.G) & 0b11) << 6),
								B: (uint16(c0.B) << 8) | (uint16(c1.B) & 0b11) | ((uint16(c2.B) & 0b11) << 2) |
									((uint16(c3.B) & 0b11) << 4) | ((uint16(c4.B) & 0b11) << 6),
								A: 0xFFFF,
							})
						}
					}

					encodeImage(packed, packedPath)
					die(os.Remove(unpackedPath))
				}
			}(i))
		}
	})
}

func packVideo() {
	fps := meta.FPS * 2
	outputPath := filepath.Join(filepath.Dir(inputPath), fmt.Sprintf("%s_%dfps.mp4", filepath.Base(projectDir), fps))

	log.Printf("Packing video frames from: %s to: %s.\n", projectDir, outputPath)
	cmd := exec.Command(
		"ffmpeg", "-y",
		"-i", inputPath,
		"-framerate", strconv.Itoa(fps),
		"-color_primaries", "bt2020",
		"-color_trc", "arib-std-b67",
		"-i", filepath.Join(projectDir, "p-%08d.png"),
		"-map", "0:a:0",
		"-map", "1:v:0",
		"-map_metadata", "0",
		"-movflags", "use_metadata_tags",
		"-map_metadata:s:d", "0:s:d",
		"-map_metadata:s:a", "0:s:a",
		"-map_metadata:s:v", "0:s:v",
		"-metadata", fmt.Sprintf("com.android.capture.fps=%d.000000", fps),
		"-c:a", "copy",
		"-c:v", "hevc",
		"-crf", "18",
		"-pix_fmt", "yuv420p10le",
		"-colorspace", "bt2020nc",
		outputPath,
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	die(cmd.Run())
}

func main() {
	initialize()

	unpackVideo()
	unpackFrames()
	interpolateFrames()
	packFrames()
	packVideo()

	die(os.RemoveAll(projectDir))
}
