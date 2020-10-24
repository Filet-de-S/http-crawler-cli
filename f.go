package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"time"
)

func scanf(r io.Reader, f string, a ...interface{}) error {
	_, err := fmt.Fscanf(r, f, a...)
	return err
}

func main() {
	minHalt := int64(time.Millisecond * time.Duration(50))
	fmt.Println(time.Duration(minHalt))
	return
	//
	f, err := os.Open("huj")
	if err != nil {
		fmt.Println("CANT OPEN FILE", err)
	}
	r := bufio.NewReader(f)
	defer f.Close()
	m := map[string]struct{}{}

	var depth int
	var url string

	for ; scanf(r, "%d %s\n", &depth, &url) == nil; {
		m[url] = struct{}{}
	}

	fmt.Println("err:", err, "\nLen:", len(m))
//	https://en.wikipedia.org/w/index.php?title=Money_Heist&amp;action=edit
//	https://en.wikipedia.org/w/index.php?title=Money_Heist&amp;action=edit

}
