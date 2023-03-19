// Copyright 2017-2021 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"

	huproxy "github.com/google/huproxy/lib"
)

var (
	listen           = flag.String("listen", "127.0.0.1:8086", "Address to listen to.")
	dialTimeout      = flag.Duration("dial_timeout", 10*time.Second, "Dial timeout.")
	handshakeTimeout = flag.Duration("handshake_timeout", 10*time.Second, "Handshake timeout.")
	writeTimeout     = flag.Duration("write_timeout", 10*time.Second, "Write timeout.")
	url              = flag.String("url", "proxy", "Path to listen to.")

	upgrader websocket.Upgrader
)

func handleProxy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// Upgrade the HTTP connection to a websocket connection.
	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Warningf("Failed to upgrade to websockets: %v", err)
		return
	}
	defer wsConn.Close()

	// receives the request from rpi websocket client, start a TCP server so other clients on the same network can connect to it
	// and then forwards the request to the TCP server and forwards the response back to the rpi websocket client
	tcpListener, err := net.Listen("tcp", "9001")
	if err != nil {
		return
	}

	// start a goroutine to handle the TCP server
	go func() {
		for {
			tcpConn, err := tcpListener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer tcpConn.Close()
				// handle the TCP connection
				// read the request from the TCP connection
				// copy websocket conn bytes to tcp conn
				go func() {
					for {
						select {
						case <-ctx.Done():
							return
						default:
							for {
								mt, r, err := wsConn.NextReader()
								if websocket.IsCloseError(err,
									websocket.CloseNormalClosure,   // Normal.
									websocket.CloseAbnormalClosure, // OpenSSH killed proxy client.
								) {
									return
								}
								if err != nil {
									log.Errorf("nextreader: %v", err)
									return
								}
								if mt != websocket.BinaryMessage {
									log.Errorf("received non-binary websocket message")
									return
								}
								if _, err := io.Copy(tcpConn, r); err != nil {
									log.Warningf("Reading from websocket: %v", err)
									cancel()
								}
							}
						}
					}
				}()
				// copy tcp conn bytes to websocket conn
				for {
					select {
					case <-ctx.Done():
						return
					default:
						buf := make([]byte, 1024)
						n, err := tcpConn.Read(buf)
						if err != nil {
							log.Warningf("Reading from file: %v", err)
							cancel()
							return
						}
						if err := wsConn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
							log.Warningf("Writing to websocket: %v", err)
							cancel()
							return
						}
					}
				}
			}()
		}
	}()
}

func main() {
	flag.Parse()

	upgrader = websocket.Upgrader{
		ReadBufferSize:   1024,
		WriteBufferSize:  1024,
		HandshakeTimeout: *handshakeTimeout,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	log.Infof("huproxy %s", huproxy.Version)
	m := mux.NewRouter()
	m.HandleFunc(fmt.Sprintf("/%s/{host}/{port}", *url), handleProxy)
	s := &http.Server{
		Addr:           *listen,
		Handler:        m,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	log.Fatal(s.ListenAndServe())
}
