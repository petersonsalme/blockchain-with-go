package main

import (
	"log"

	"github.com/joho/godotenv"
	"github.com/petersonsalme/blockchain-with-go/server"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal(err)
	}

	log.Fatal(server.Run())
}
