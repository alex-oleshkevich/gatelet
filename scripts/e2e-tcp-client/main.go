package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"time"
)

func main() {
	addr := flag.String("addr", "", "server address")
	message := flag.String("message", "ping\n", "message to send")
	timeout := flag.Duration("timeout", 5*time.Second, "connection timeout")
	flag.Parse()

	if *addr == "" {
		log.Fatal("-addr is required")
	}

	conn, err := net.DialTimeout("tcp", *addr, *timeout)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(*timeout))
	if _, err := conn.Write([]byte(*message)); err != nil {
		log.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	_, _ = fmt.Fprint(os.Stdout, line)
}
