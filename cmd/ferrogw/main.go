// Package main is the ferrogw binary: a thin caller of the importable
// program in package run, which is what a composed binary calls too.
package main

import "github.com/ferro-labs/ai-gateway/run"

func main() {
	run.Main()
}
