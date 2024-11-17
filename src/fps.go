package src

import (
	"fmt"
	"gopkg.in/Knetic/govaluate.v2"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const FrameFactor = 2
const DirFrames = "frames"
const DirOutput = "output"
const DirVars0 = "vars0"
const DirVars1 = "vars1"
const DirVars2 = "vars2"
const DirVars3 = "vars3"
const DirVars4 = "vars4"

var cpus = runtime.NumCPU()
var leaseMutex = sync.Mutex{}
var leaseRunning = 0

type Metadata struct {
	Width  int
	Height int
	Frames int
	FPS    int
}

func pad(value, length int) string {
	return fmt.Sprintf("%0*d", length, value)
}

func eval(value string) int {
	expr, err := govaluate.NewEvaluableExpression(value)
	Die(err)

	result, err := expr.Evaluate(map[string]interface{}{})
	Die(err)

	return int(result.(float64))
}

func run(job func()) {
	for {
		leaseMutex.Lock()
		running := leaseRunning
		leaseMutex.Unlock()

		if running >= cpus {
			time.Sleep(time.Millisecond)
			continue
		}

		leaseMutex.Lock()
		leaseRunning++
		leaseMutex.Unlock()

		go func() {
			job()

			leaseMutex.Lock()
			leaseRunning--
			leaseMutex.Unlock()
		}()

		return
	}
}

func getDir(paths ...string) string {
	path := filepath.Join(paths...)

	err := os.MkdirAll(path, os.ModePerm)
	Die(err)

	return path
}

func getName(path string) string {
	return filepath.Base(path[:len(path)-len(filepath.Ext(path))])
}

func decodeImage(file string) image.Image {
	reader, err := os.Open(file)
	Die(err)

	img, err := png.Decode(reader)
	Die(err)

	Die(reader.Close())
	return img
}

func encodeImage(img image.Image, file string) {
	writer, err := os.Create(file)
	Die(err)

	Die(png.Encode(writer, img))
	Die(writer.Close())
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
	Die(err)

	return strings.Trim(string(out), "\r\n")
}

func Die(err error) {
	if nil != err {
		log.Fatal(err)
	}
}

func GetMetadata(file string) *Metadata {
	log.Printf("Analyzing video file: %s.\n", file)
	meta := &Metadata{
		Frames: int(eval(probe(file, "stream=nb_frames"))),
		FPS:    eval(probe(file, "stream=avg_frame_rate")),
	}

	rotation := probe(file, "stream_side_data=rotation")
	if "" == rotation || "-180" == rotation || "180" == rotation {
		meta.Width = int(eval(probe(file, "stream=width")))
		meta.Height = int(eval(probe(file, "stream=height")))
	} else if "-90" == rotation || "90" == rotation || "-270" == rotation || "270" == rotation {
		meta.Width = int(eval(probe(file, "stream=height")))
		meta.Height = int(eval(probe(file, "stream=width")))
	} else {
		log.Fatalf("Unknown rotation value: %s.\n", rotation)
	}

	return meta
}

func UnpackVideo(file string) {
	framesPath := getDir(getDir(filepath.Dir(file), getName(file)), DirFrames)

	log.Printf("Unpacking video frames from: %s to: %s.\n", file, framesPath)
	cmd := exec.Command("ffmpeg", "-i", file, "-pix_fmt", "rgb48be", "-y", filepath.Join(framesPath, "%08d.png"))

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	Die(cmd.Run())
}

func UnpackFrames(file string, meta *Metadata) {
	dir := getDir(filepath.Dir(file), getName(file))

	framesPath := getDir(dir, DirFrames)
	vars0Path := getDir(dir, DirVars0)
	vars1Path := getDir(dir, DirVars1)
	vars2Path := getDir(dir, DirVars2)
	vars3Path := getDir(dir, DirVars3)
	vars4Path := getDir(dir, DirVars4)

	for i := 1; i <= meta.Frames; i++ {
		run(func(i int) func() {
			return func() {
				log.Printf("Unpacking frame %d of %d [%.2f%%].\n", i, meta.Frames, 100*float64(i)/float64(meta.Frames))

				frame := decodeImage(filepath.Join(framesPath, pad(i, 8)+".png"))

				var0 := image.NewRGBA(image.Rect(0, 0, meta.Width, meta.Height))
				var1 := image.NewRGBA(image.Rect(0, 0, meta.Width, meta.Height))
				var2 := image.NewRGBA(image.Rect(0, 0, meta.Width, meta.Height))
				var3 := image.NewRGBA(image.Rect(0, 0, meta.Width, meta.Height))
				var4 := image.NewRGBA(image.Rect(0, 0, meta.Width, meta.Height))

				for x := 0; x < meta.Width; x++ {
					for y := 0; y < meta.Height; y++ {
						c := frame.(*image.RGBA64).RGBA64At(x, y)

						var0.Set(x, y, color.RGBA{
							R: uint8((c.R >> 8) & 0b11111111),
							G: uint8((c.G >> 8) & 0b11111111),
							B: uint8((c.B >> 8) & 0b11111111),
							A: 0xFF,
						})

						var1.Set(x, y, color.RGBA{
							R: uint8((c.R>>8)&0b11111100) | uint8(c.R&0b11),
							G: uint8((c.G>>8)&0b11111100) | uint8(c.G&0b11),
							B: uint8((c.B>>8)&0b11111100) | uint8(c.B&0b11),
							A: 0xFF,
						})

						var2.Set(x, y, color.RGBA{
							R: uint8((c.R>>8)&0b11111100) | uint8((c.R>>2)&0b11),
							G: uint8((c.G>>8)&0b11111100) | uint8((c.G>>2)&0b11),
							B: uint8((c.B>>8)&0b11111100) | uint8((c.B>>2)&0b11),
							A: 0xFF,
						})

						var3.Set(x, y, color.RGBA{
							R: uint8((c.R>>8)&0b11111100) | uint8((c.R>>4)&0b11),
							G: uint8((c.G>>8)&0b11111100) | uint8((c.G>>4)&0b11),
							B: uint8((c.B>>8)&0b11111100) | uint8((c.B>>4)&0b11),
							A: 0xFF,
						})

						var4.Set(x, y, color.RGBA{
							R: uint8((c.R>>8)&0b11111100) | uint8((c.R>>6)&0b11),
							G: uint8((c.G>>8)&0b11111100) | uint8((c.G>>6)&0b11),
							B: uint8((c.B>>8)&0b11111100) | uint8((c.B>>6)&0b11),
							A: 0xFF,
						})
					}
				}

				encodeImage(var0, filepath.Join(vars0Path, pad(i, 8)+".png"))
				encodeImage(var1, filepath.Join(vars1Path, pad(i, 8)+".png"))
				encodeImage(var2, filepath.Join(vars2Path, pad(i, 8)+".png"))
				encodeImage(var3, filepath.Join(vars3Path, pad(i, 8)+".png"))
				encodeImage(var4, filepath.Join(vars4Path, pad(i, 8)+".png"))
			}
		}(i))
	}
}

func PackFrames(file string, meta *Metadata) {
	dir := getDir(filepath.Dir(file), getName(file))

	outputPath := getDir(dir, DirOutput)
	vars0Path := getDir(dir, DirVars0+"-temp", "interp")
	vars1Path := getDir(dir, DirVars1+"-temp", "interp")
	vars2Path := getDir(dir, DirVars2+"-temp", "interp")
	vars3Path := getDir(dir, DirVars3+"-temp", "interp")
	vars4Path := getDir(dir, DirVars4+"-temp", "interp")

	for i := 1200; i <= meta.Frames*FrameFactor-(FrameFactor-1); i++ {
		run(func(i int) func() {
			return func() {
				log.Printf("Packing frame %d of %d [%.2f%%].\n", i, meta.Frames*FrameFactor-(FrameFactor-1),
					100*float64(i)/float64(meta.Frames*FrameFactor-(FrameFactor-1)))

				output := image.NewRGBA64(image.Rect(0, 0, meta.Width, meta.Height))

				var0 := decodeImage(filepath.Join(vars0Path, pad(i, 8)+".png"))
				var1 := decodeImage(filepath.Join(vars1Path, pad(i, 8)+".png"))
				var2 := decodeImage(filepath.Join(vars2Path, pad(i, 8)+".png"))
				var3 := decodeImage(filepath.Join(vars3Path, pad(i, 8)+".png"))
				var4 := decodeImage(filepath.Join(vars4Path, pad(i, 8)+".png"))

				for x := 0; x < meta.Width; x++ {
					for y := 0; y < meta.Height; y++ {
						c0 := var0.(*image.NRGBA).NRGBAAt(x, y)
						c1 := var1.(*image.NRGBA).NRGBAAt(x, y)
						c2 := var2.(*image.NRGBA).NRGBAAt(x, y)
						c3 := var3.(*image.NRGBA).NRGBAAt(x, y)
						c4 := var4.(*image.NRGBA).NRGBAAt(x, y)

						output.Set(x, y, color.RGBA64{
							R: (uint16(c0.R) << 8) | (uint16(c1.R) & 0b11) | ((uint16(c2.R) & 0b11) << 2) | ((uint16(c3.R) & 0b11) << 4) | ((uint16(c4.R) & 0b11) << 6),
							G: (uint16(c0.G) << 8) | (uint16(c1.G) & 0b11) | ((uint16(c2.G) & 0b11) << 2) | ((uint16(c3.G) & 0b11) << 4) | ((uint16(c4.G) & 0b11) << 6),
							B: (uint16(c0.B) << 8) | (uint16(c1.B) & 0b11) | ((uint16(c2.B) & 0b11) << 2) | ((uint16(c3.B) & 0b11) << 4) | ((uint16(c4.B) & 0b11) << 6),
							A: 0xFFFF,
						})
					}
				}

				encodeImage(output, filepath.Join(outputPath, pad(i, 8)+".png"))
			}
		}(i))
	}
}

func PackVideo(file string, meta *Metadata) {
	fps := meta.FPS * FrameFactor

	framesPath := filepath.Join(getDir(filepath.Dir(file), getName(file)), DirOutput)
	videoPath := filepath.Join(filepath.Dir(file), getName(file)+fmt.Sprintf("_%dfps.mp4", fps))

	log.Printf("Packing video frames from: %s to: %s.\n", framesPath, videoPath)
	cmd := exec.Command(
		"ffmpeg", "-y",
		"-i", file,
		"-framerate", strconv.Itoa(int(fps)),
		"-color_primaries", "bt2020",
		"-color_trc", "arib-std-b67",
		"-i", filepath.Join(framesPath, "%08d.png"),
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
		videoPath,
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	Die(cmd.Run())
}

func Prune(file string) {
	log.Printf("Pruning temporary files for: %s.\n", file)
	Die(os.RemoveAll(filepath.Join(filepath.Dir(file), getName(file))))
}

func Finish() {
	for {
		leaseMutex.Lock()
		running := leaseRunning
		leaseMutex.Unlock()

		if 0 < running {
			time.Sleep(time.Millisecond)
			continue
		}

		return
	}
}
