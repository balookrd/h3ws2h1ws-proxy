package main

import (
	"log"

	"h3ws2h1ws-proxy/internal"
)

func main() {
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
