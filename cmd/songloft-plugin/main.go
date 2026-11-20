package main

import (
	"log"

	"github.com/waygu-music-plugin/sdk/desktop"
	"github.com/waygu-music-plugin/songloft"
)

func main() {
	if err := desktop.RunPlatformPlugin(songloft.Registration()); err != nil {
		log.Fatal(err)
	}
}
