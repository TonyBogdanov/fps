package main

import (
	"fps/src"
	"log"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		log.Println("No input provided, drop a video file onto this executable.")
		time.Sleep(10 * time.Second)

		return
	}

	input, err := filepath.Abs(os.Args[1])
	src.Die(err)

	meta := src.GetMetadata(input)

	src.UnpackVideo(input)
	src.UnpackFrames(input, meta)
	src.Finish()
}
