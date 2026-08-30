//go:build ignore

package main

import (
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"runtime"
)

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	addr := "localhost:8080"
	if p := os.Getenv("PORT"); p != "" {
		addr = "localhost:" + p
	}
	if err := mime.AddExtensionType(".wasm", "application/wasm"); err != nil {
		log.Fatal(err)
	}
	url := "http://" + addr
	fmt.Println("Serving", dir, "at", url)
	go openBrowser(url)
	log.Fatal(http.ListenAndServe(addr, http.FileServer(http.Dir(dir))))
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Run()
}
