package main

import (
	"fmt"

	"github.com/romshark/gqlhash/parser"
)

type Writer []byte

func (w *Writer) Write(data []byte) (int, error) {
	*w = append(*w, data...)
	return len(data), nil
}

func (w *Writer) Reset()              { panic("nope") }
func (w *Writer) Sum(b []byte) []byte { return append(b, *w...) }

func (w *Writer) String() string { return string(*w) }

func main() {
	printTokens(`{x(a:"/ab/6bar")}`)
	printTokens(`{x(a:"",b:"bar")}`)
}

func printTokens(input string) {
	w := new(Writer)
	if err := parser.ReadDocument(w, []byte(input)); err != nil {
		panic(err)
	}
	fmt.Println(w.String())
}
