package main

import (
	"encoding/binary"
	"fmt"
	"gopkg.in/Knetic/govaluate.v2"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const RifePath = "E:\\rife-ncnn-vulkan\\src\\out\\build\\x64-Debug\\rife-ncnn-vulkan.exe"
const Parallelism = 3
const ProgressBatchSize = 4 * Parallelism

var input string
var meta *Metadata

type Metadata struct {
	Width  uint64
	Height uint64
	Frames uint64
	FPSH   uint64
	FPSL   uint64
}

type Interpolation struct {
	Id    uint64
	Left  image.RGBA
	Right image.RGBA
}

type Interpolator struct {
	Batch uint64
	Run   Interpolate
}

type Interpolate func(task Interpolation) Interpolation

func (o Metadata) FPS() float64 {
	return float64(o.FPSH) / float64(o.FPSL)
}

func (o Metadata) DoubleFPS() float64 {
	return 2.0 * o.FPS()
}

func die(err error) {
	if nil != err {
		log.Println(err)
		log.Println("--- ERROR ---")

		os.Exit(1)
	}
}

func eval(value string) float64 {
	expr, err := govaluate.NewEvaluableExpression(value)
	die(err)

	result, err := expr.Evaluate(map[string]interface{}{})
	die(err)

	return result.(float64)
}

func parseUint64(value string) uint64 {
	result, err := strconv.Atoi(value)
	die(err)

	return uint64(result)
}

func parseRatio(value string) (uint64, uint64) {
	parts := strings.Split(value, "/")
	if 2 != len(parts) {
		die(fmt.Errorf("Invalid ratio value: %s.\n", value))
	}

	return parseUint64(parts[0]), parseUint64(parts[1])
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

func readBytes(reader io.Reader, size int) []byte {
	bytes := make([]byte, size)
	read, err := io.ReadFull(reader, bytes)
	die(err)

	if size != read {
		die(fmt.Errorf("failed to read %d bytes, read %d instead", size, read))
	}

	return bytes
}

func writeBytes(writer io.Writer, bytes []byte) {
	written, err := writer.Write(bytes)
	die(err)

	if len(bytes) != written {
		die(fmt.Errorf("failed to write %d bytes, wrote %d instead", len(bytes), written))
	}
}

func writeUint64(writer io.Writer, value uint64) {
	bytes := make([]byte, 8)
	binary.BigEndian.PutUint64(bytes, value)

	writeBytes(writer, bytes)
}

func progress(total uint64, fn func(tick func())) {
	var mx sync.Mutex
	var current uint64

	var measures []time.Time

	fn(func() {
		mx.Lock()
		defer mx.Unlock()

		current++
		measures = append(measures, time.Now())

		for ProgressBatchSize < len(measures) {
			measures = measures[1:]
		}

		fps := "--.-"
		remain := "--:--"

		if ProgressBatchSize == len(measures) {
			elapsed := measures[len(measures)-1].Sub(measures[0])
			speed := float64(ProgressBatchSize) / elapsed.Seconds()

			fps = fmt.Sprintf("%4.1f", speed)
			remain = fmt.Sprintf("%02d:%02d", int(float64(total-current)/speed)/60, int(float64(total-current)/speed)%60)
		}

		log.Printf(
			"Progress: %7s%% [%s/s | %s].\n",
			fmt.Sprintf("%.3f", 100*float64(current)/float64(total)), fps, remain,
		)
	})
}

func encodePackedImage(img image.RGBA64) []byte {
	width := int(meta.Width)
	height := int(meta.Height)

	bytes := make([]byte, 0, width*height*3)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, _ := img.At(x, y).RGBA()

			bytes = append(
				bytes,
				uint8(r>>8),
				uint8(r),
				uint8(g>>8),
				uint8(g),
				uint8(b>>8),
				uint8(b),
			)
		}
	}

	return bytes
}

func encodeUnpackedImage(img image.RGBA) []byte {
	width := int(meta.Width * 5)
	height := int(meta.Height)

	bytes := make([]byte, 0, width*height*3)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, _ := img.At(x, y).RGBA()

			// for some reason rife-ncnn-vulkan expects images in BGR, not RGB
			bytes = append(bytes, byte(b/256), byte(g/256), byte(r/256))
		}
	}

	return bytes
}

func decodeUnpackedImage(bytes []byte) image.RGBA {
	width := int(meta.Width * 5)
	height := int(meta.Height)

	img := image.NewRGBA(image.Rect(0, 0, width, height))

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			offset := (y*width + x) * 3

			// for some reason rife-ncnn-vulkan expects images in BGR, not RGB
			img.Set(x, y, color.RGBA{
				R: bytes[offset+2],
				G: bytes[offset+1],
				B: bytes[offset],
				A: 0xFF,
			})
		}
	}

	return *img
}

func createInterpolator(frames uint64) Interpolate {
	cmd := exec.Command(RifePath)
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	die(err)

	stdin, err := cmd.StdinPipe()
	die(err)

	die(cmd.Start())

	writeUint64(stdin, frames)
	writeUint64(stdin, meta.Width*5)
	writeUint64(stdin, meta.Height)

	var mx sync.Mutex
	var interpolated uint64

	return func(task Interpolation) Interpolation {
		mx.Lock()
		defer mx.Unlock()

		writeBytes(stdin, encodeUnpackedImage(task.Left))
		writeBytes(stdin, encodeUnpackedImage(task.Right))

		result := decodeUnpackedImage(readBytes(stdout, int(meta.Width*meta.Height*15)))
		interpolated++

		if interpolated == frames {
			die(cmd.Wait())
		}

		return Interpolation{
			Id:    task.Id,
			Left:  result,
			Right: task.Right,
		}
	}
}

func debugRGBA(file string, items <-chan image.RGBA) <-chan image.RGBA {
	out := make(chan image.RGBA)

	go func() {
		defer close(out)

		i := 0
		for item := range items {
			handle, err := os.OpenFile(file+"_"+strconv.Itoa(i)+".png", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
			die(err)

			die(png.Encode(handle, &item))
			die(handle.Close())

			out <- item
			i++
		}
	}()

	return out
}

func debugRGBA64(file string, items <-chan image.RGBA64) <-chan image.RGBA64 {
	out := make(chan image.RGBA64)

	go func() {
		defer close(out)

		i := 0
		for item := range items {
			handle, err := os.OpenFile(file+"_"+strconv.Itoa(i)+".png", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
			die(err)

			die(png.Encode(handle, &item))
			die(handle.Close())

			out <- item
			i++
		}
	}()

	return out
}

func decodeVideo() <-chan image.RGBA64 {
	out := make(chan image.RGBA64)
	cmd := exec.Command(
		"ffmpeg",
		"-i", input,
		"-pix_fmt", "rgb48be",
		"-f", "image2pipe",
		"-vcodec", "rawvideo",
		"-",
	)

	stdout, err := cmd.StdoutPipe()
	die(err)

	die(cmd.Start())

	go func() {
		defer close(out)

		width := int(meta.Width)
		height := int(meta.Height)

		chunkSize := width * height * 6
		buffer := make([]byte, chunkSize)

		var accumulator []byte

		for {
			read, err := stdout.Read(buffer)

			if 0 < read {
				accumulator = append(accumulator, buffer[:read]...)

				for chunkSize <= len(accumulator) {
					chunk := make([]byte, chunkSize)
					copy(chunk, accumulator[:chunkSize])

					packed := image.NewRGBA64(image.Rect(0, 0, width, height))
					for y := 0; y < height; y++ {
						for x := 0; x < width; x++ {
							offset := (y*width + x) * 6

							r := uint16(chunk[offset])<<8 | uint16(chunk[offset+1])
							g := uint16(chunk[offset+2])<<8 | uint16(chunk[offset+3])
							b := uint16(chunk[offset+4])<<8 | uint16(chunk[offset+5])

							packed.SetRGBA64(x, y, color.RGBA64{R: r, G: g, B: b, A: 0xFFFF})
						}
					}

					out <- *packed
					accumulator = accumulator[chunkSize:]
				}
			}

			if err == io.EOF {
				break
			} else {
				die(err)
			}
		}

		die(cmd.Wait())
	}()

	return out
}

func encodeVideo(input, output string, tick func(), in <-chan image.RGBA64) {
	cmd := exec.Command(
		"ffmpeg",
		"-i", input,
		"-f", "rawvideo",
		"-pix_fmt", "rgb48be",
		"-video_size", fmt.Sprintf("%dx%d", meta.Width, meta.Height),
		"-framerate", fmt.Sprintf("%.6f", meta.DoubleFPS()),
		"-color_primaries", "bt2020",
		"-color_trc", "arib-std-b67",
		"-i", "-",
		"-map", "0:a:0",
		"-map", "1:v:0",
		"-map_metadata", "0",
		"-movflags", "use_metadata_tags",
		"-map_metadata:s:a", "0:s:a",
		"-map_metadata:s:v", "0:s:v",
		"-c:a", "copy",
		"-c:v", "hevc",
		"-crf", "18",
		"-pix_fmt", "yuv420p10le",
		"-colorspace", "bt2020nc",
		"-y", output,
	)

	stdin, err := cmd.StdinPipe()
	die(err)

	go func() {
		for img := range in {
			writeBytes(stdin, encodePackedImage(img))
			tick()
		}

		die(stdin.Close())
	}()

	die(cmd.Run())
}

func unpackImage(in <-chan image.RGBA64) <-chan image.RGBA {
	out := make(chan image.RGBA)

	go func() {
		defer close(out)

		width := int(meta.Width)
		height := int(meta.Height)

		for packed := range in {
			unpacked := image.NewRGBA(image.Rect(0, 0, width*5, height))

			for x := 0; x < width; x++ {
				for y := 0; y < height; y++ {
					c := packed.RGBA64At(x, y)

					unpacked.Set(x, y, color.RGBA{
						R: uint8((c.R >> 8) & 0b11111111),
						G: uint8((c.G >> 8) & 0b11111111),
						B: uint8((c.B >> 8) & 0b11111111),
						A: 0xFF,
					})

					unpacked.Set(x+width, y, color.RGBA{
						R: uint8((c.R>>8)&0b11111100) | uint8(c.R&0b11),
						G: uint8((c.G>>8)&0b11111100) | uint8(c.G&0b11),
						B: uint8((c.B>>8)&0b11111100) | uint8(c.B&0b11),
						A: 0xFF,
					})

					unpacked.Set(x+width*2, y, color.RGBA{
						R: uint8((c.R>>8)&0b11111100) | uint8((c.R>>2)&0b11),
						G: uint8((c.G>>8)&0b11111100) | uint8((c.G>>2)&0b11),
						B: uint8((c.B>>8)&0b11111100) | uint8((c.B>>2)&0b11),
						A: 0xFF,
					})

					unpacked.Set(x+width*3, y, color.RGBA{
						R: uint8((c.R>>8)&0b11111100) | uint8((c.R>>4)&0b11),
						G: uint8((c.G>>8)&0b11111100) | uint8((c.G>>4)&0b11),
						B: uint8((c.B>>8)&0b11111100) | uint8((c.B>>4)&0b11),
						A: 0xFF,
					})

					unpacked.Set(x+width*4, y, color.RGBA{
						R: uint8((c.R>>8)&0b11111100) | uint8((c.R>>6)&0b11),
						G: uint8((c.G>>8)&0b11111100) | uint8((c.G>>6)&0b11),
						B: uint8((c.B>>8)&0b11111100) | uint8((c.B>>6)&0b11),
						A: 0xFF,
					})
				}
			}

			out <- *unpacked
		}
	}()

	return out
}

func packImage(in <-chan image.RGBA) <-chan image.RGBA64 {
	out := make(chan image.RGBA64)

	go func() {
		defer close(out)

		width := int(meta.Width)
		height := int(meta.Height)

		for unpacked := range in {
			packed := image.NewRGBA64(image.Rect(0, 0, width, height))

			for x := 0; x < width; x++ {
				for y := 0; y < height; y++ {
					c0 := unpacked.RGBAAt(x, y)
					c1 := unpacked.RGBAAt(x+width, y)
					c2 := unpacked.RGBAAt(x+2*width, y)
					c3 := unpacked.RGBAAt(x+3*width, y)
					c4 := unpacked.RGBAAt(x+4*width, y)

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

			out <- *packed
		}
	}()

	return out
}

func interpolateImage(in <-chan image.RGBA) <-chan image.RGBA {
	out := make(chan image.RGBA)

	tasks := make(chan Interpolation)
	results := make(chan Interpolation, Parallelism)

	if Parallelism > meta.Frames-1 {
		die(fmt.Errorf("parallelism cannot be greater than the number of frames to be interpolated"))
	}

	// Create Parallelism number of interpolators & assign a batch number to signify how many frames should be handled.
	var interpolators []Interpolator
	batchSize := (meta.Frames - 1) / Parallelism

	for i := 0; i < Parallelism; i++ {
		batch := batchSize
		if 0 == i {
			batch = batchSize + meta.Frames - batchSize*Parallelism - 1
		}

		interpolators = append(interpolators, Interpolator{
			Batch: batch,
			Run:   createInterpolator(batch),
		})
	}

	// Traverse results & push them to the output channel.
	// Re-schedule results received out of order.
	// Once the expected number of results are received, close both the results & output channels.
	go func() {
		defer close(results)
		defer close(out)

		var i uint64
		var nextId uint64 = 2

		for ; i < meta.Frames-1; i++ {
			result := <-results

			if result.Id != nextId {
				i--
				results <- result

				continue
			}

			out <- result.Left
			out <- result.Right

			nextId++
		}
	}()

	// Traverse tasks, interpolate them and push the result to the results channel.
	for _, interpolator := range interpolators {
		go func(interpolator Interpolator) {
			var i uint64
			for ; i < interpolator.Batch; i++ {
				results <- interpolator.Run(<-tasks)
			}
		}(interpolator)
	}

	// Traverse input frames, create tasks and push them the tasks queue.
	// The very first frame is pushed to the output channel immediately.
	go func() {
		defer close(tasks)

		var left image.RGBA
		var id uint64

		for right := range in {
			id++

			if 1 == id {
				left = right
				out <- right

				continue
			}

			tasks <- Interpolation{
				Id:    id,
				Left:  left,
				Right: right,
			}

			left = right
		}
	}()

	return out
}

func main() {
	var err error
	if 1 >= len(os.Args) {
		return
	}

	os.Args = os.Args[1:]
	input, err = filepath.Abs(os.Args[0])
	die(err)

	log.Printf("Processing video file: %s.\n", input)
	meta = &Metadata{}

	meta.Frames = parseUint64(probe(input, "stream=nb_frames"))
	meta.FPSH, meta.FPSL = parseRatio(probe(input, "stream=avg_frame_rate"))

	ext := filepath.Ext(input)
	base := filepath.Base(input)
	output := filepath.Join(filepath.Dir(input), base[:len(base)-len(ext)]+fmt.Sprintf("_%.2ffps.mp4", meta.DoubleFPS()))

	rotation := probe(input, "stream_side_data=rotation")
	if "" == rotation || "-180" == rotation || "180" == rotation {
		meta.Width = uint64(eval(probe(input, "stream=width")))
		meta.Height = uint64(eval(probe(input, "stream=height")))
	} else if "-90" == rotation || "90" == rotation || "-270" == rotation || "270" == rotation {
		meta.Width = uint64(eval(probe(input, "stream=height")))
		meta.Height = uint64(eval(probe(input, "stream=width")))
	} else {
		die(fmt.Errorf("Unknown rotation value: %s.\n", rotation))
	}

	progress(meta.Frames*2-1, func(tick func()) {
		encodeVideo(input, output, tick, packImage(interpolateImage(unpackImage(decodeVideo()))))
	})

	main()
}
