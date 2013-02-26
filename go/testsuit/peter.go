package main

import (
	"fmt"
	"time"
)

var c chan int

func ready(index int) {
	time.Sleep(int64(index) * 1e9)
	fmt.Println(index)
	c<-1
}

func main() {
	total := 10
	c = make(chan int)
	for i := 0; i < total; i++ {
		go ready(i + 1)
	}
	for i := 0; i < total; i++ {
		<-c
	}
}
