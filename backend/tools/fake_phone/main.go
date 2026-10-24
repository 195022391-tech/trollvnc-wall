// 联调假设备：先 API 登记，再 TCP hello+heartbeat；不推真流。
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	api := flag.String("api", "http://127.0.0.1:18082", "wall api")
	ctrl := flag.String("ctrl", "127.0.0.1:19000", "ctrl addr")
	id := flag.String("id", "phone-demo", "deviceId")
	token := flag.String("token", "demo-token", "token")
	name := flag.String("name", "DemoPhone", "name")
	flag.Parse()

	body, _ := json.Marshal(map[string]string{
		"deviceId": *id, "token": *token, "name": *name,
	})
	resp, err := http.Post(*api+"/api/devices/register", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Println("register:", err)
		os.Exit(1)
	}
	resp.Body.Close()
	fmt.Println("registered", *id)

	c, err := net.Dial("tcp", *ctrl)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer c.Close()

	hello, _ := json.Marshal(map[string]any{
		"type": "hello",
		"body": map[string]string{"deviceId": *id, "token": *token, "name": *name},
	})
	mustWrite(c, hello)

	buf := make([]byte, 4)
	if _, err := c.Read(buf); err != nil {
		fmt.Println("read hello_ok hdr:", err)
		os.Exit(1)
	}
	n := binary.BigEndian.Uint32(buf)
	payload := make([]byte, n)
	_, _ = c.Read(payload)
	fmt.Println("server:", string(payload))

	hb, _ := json.Marshal(map[string]string{"type": "heartbeat"})
	t := time.NewTicker(500 * time.Millisecond)
	for range t.C {
		if err := mustWrite(c, hb); err != nil {
			fmt.Println("hb:", err)
			return
		}
	}
}

func mustWrite(c net.Conn, payload []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := c.Write(hdr[:]); err != nil {
		return err
	}
	_, err := c.Write(payload)
	return err
}
