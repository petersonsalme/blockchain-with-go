package models

type Message struct {
	Transactions []Transaction
}

type LegacyMessage struct {
	BPM int
}
