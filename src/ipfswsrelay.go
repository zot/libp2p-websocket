package main

// Copyright 2020 Bill Burdick. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
# IPFS-P2P-WEBSOCKET

Relay p2p connections between browsers transcoding between sockets and websockets

This runs a simple websocket server and listens for websocket connections using a simple protocol:

# /ws/control
Allow browser to control the relay with JSON over a websocket
when the connection closes, clean up all of the child connections.
In this websocket connection, the client and server exchange these commands
messages, with the first byte of each message identifying the command.

# CLIENT-TO-SERVER MESSAGES
 
```
  Listen:       [1][PROTOCOL: rest]              -- request a listener for a protocol
  Stop:         [2][PROTOCOL: rest]              -- stop listening on PORT
  Close:        [3][ID: 8]                       -- close a stream
  Data:         [4][ID: 8][data: rest]           -- write data to stream
  Peer Connect: [5][PROTOCOL: STR][PEERID: rest] -- connect to another peer
```

# SERVER-TO-CLIENT MESSAGES

```
  Listener Connection:     [1][ID: 8][PROTOCOL: rest] -- new listener connection with id ID
  Connection Closed:       [2][ID: 8]                 -- connection ID closed
  Data:                    [3][ID: 8][data: rest]     -- receive data from stream with id ID
  Listen Refused:          [4][PROTOCOL: rest]        -- could not listen on PORT
  Listener Closed:         [5][PROTOCOL: rest]        -- could not listen on PORT
  Peer Connection:         [6][ID: 8]                 -- connected to a peer with id ID
  Peer Connection Refused: [7][PEERID: rest]          -- connection to peer PEERID refused
```
*/

import (
	"encoding/binary"
	"flag"
	"fmt"
	"github.com/gorilla/websocket"
	"log"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type messageType byte
const (
	cmsgListen messageType = iota
	cmsgStop
	cmsgClose
	cmsgData
	cmsgConnect
	smsgIdent = iota - cmsgConnect - 1 // restart at 0
	smsgNewConnection
	smsgConnectionClosed
	smsgData
	smsgListenRefused
	smsgListenerClosed
	smsgPeerConnection
	smsgPeerConnectionRefused
)

const (
	ipfsName = "ipfs"
	maxMessageSize = 65536 // Maximum websocket message size
)

var ipfsPath string
var peerId string

var (
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
)

type simplerCloser interface {
	close()
}

type listener struct {
	listener *net.TCPListener              // socket listener
	client *client                         // the client that owns this listener
	connections map[uint64]*net.TCPConn    // connectionID -> connection
	port int
	protocol string
}

type forwarder struct {
	port int                               // a record of the forwarder's port so we can close it later
	id uint64
	connection *net.TCPConn
	client *client
}

// client allows a browser to use the relay
type client struct {
	nextConnectionID uint64
	control *websocket.Conn                     // the client's control websocket
	listeners map[string]*listener                 // port -> listener owned by this client
	listenerConnections map[uint64]*listener    // connectionID -> listener owned by this client
	forwarders map[uint64]*forwarder            // connectionID -> forwarder
	managementChan chan func()                  // client management
	running bool
}

type relay struct {
	clients map[*websocket.Conn]*client    // id -> client
	managementChan chan func()             // client creation
}

type chanSvc interface {
	getSvcChannel() chan func()
}

func svc(s chanSvc, code func()) {
	s.getSvcChannel() <- code
}

func runSvc(s chanSvc) {
	go func() {
		for {
			cmd, ok := <- s.getSvcChannel()
			if !ok {
				break
			}
			cmd()
		}
	}()
}

func (f *forwarder) close() {
	if f.connection != nil {
		f.connection.Close()
		f.connection = nil
		delete(f.client.forwarders, f.id)
	}
}

func createListener() *listener {
	l := new(listener)
	l.connections = make(map[uint64]*net.TCPConn)
	return l
}

// process connections as they come in
func (l *listener) run(c *client) {
	go func() {
		var con *net.TCPConn
		var err error = nil

		for err != nil {
			con, err = l.listener.AcceptTCP()
			svc(c, func() {
				if err != nil {
					l.close()
				} else {
					id := c.newConnectionID()
					l.connections[id] = con
					c.readSocket(id, con)
				}
			})
		}
	}()
}

func (l *listener) close() {
	l.listener.Close()
	for id := range l.connections {
		l.closeConnection(id)
	}
	exec.Command(ipfsPath, "p2p", "close", "-l", strconv.Itoa(l.port)).Run()
	l.client.writeMessage(smsgListenerClosed, []byte(l.protocol))
	delete(l.client.listeners, l.protocol)
}

func (l *listener) closeConnection(id uint64) {
	l.connections[id].Close()
	delete(l.connections, id)
	delete(l.client.listenerConnections, id)
}

func createClient() *client {
	c := new(client)
	c.listeners = make(map[string]*listener)
	c.listenerConnections = make(map[uint64]*listener)
	c.forwarders = make(map[uint64]*forwarder)
	c.managementChan = make(chan func())
	return c
}

func (c *client) getSvcChannel() chan func() {
	return c.managementChan
}

func (c *client) readSocket(conID uint64, con *net.TCPConn) {
	buf := [maxMessageSize]byte{byte(smsgData)}
	binary.BigEndian.PutUint64(buf[1:], conID)
	con.SetReadBuffer(maxMessageSize)
	con.SetWriteBuffer(maxMessageSize)
	go func() {
		var len int
		var err error = nil

		for err != nil {
			len, err = con.Read(buf[9:])
			svc(c, func() {
				if err != nil {
					buf[0] = byte(smsgConnectionClosed) // conID is already in buf[1:9]
					c.writeFullMessage(buf[0:9])
					c.closeStream(conID)
				} else {
					c.writeFullMessage(buf[0:9 + len])
				}
			})
		}
	}()
}

func (c *client) close() {
	c.running = false
	closable := make(map[int]simplerCloser) // collect into this map because Close() alters maps
	for _, v := range c.listeners {
		closable[len(closable)] = v
	}
	for _, v := range c.forwarders {
		closable[len(closable)] = v
	}
	for _, cl := range closable {
		cl.close()
	}
}

func (c *client) readWebsocket() {
	go func() {
		_, data, err := c.control.ReadMessage()
		svc(c, func() {
			if err == nil && len(data) > 0 {
				switch messageType(data[0]) {
				case cmsgListen: c.listen(string(data[1:]))
				case cmsgStop: c.stopListener(string(data[1:]))
				case cmsgClose: c.closeStream(binary.BigEndian.Uint64(data[1:9]))
				case cmsgData: c.sendData(binary.BigEndian.Uint64(data[1:9]), data[9:])
				case cmsgConnect:
					prot, peerid := getString(data[6:])
					c.connect(prot, string(peerid))
				}
				c.readWebsocket()
			} else {
				if err != nil {
					log.Printf("error: %v\n", err)
				}
				c.close()
			}
		})
	}()
}

// LISTEN API METHOD
func (c *client) listen(protocol string) {
	var port int
	var lisnr *net.TCPListener
	var err error
	for port = 10000; port < 65536; port++ {
		lisnr, err = net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IP{127,0,0,1}, Port: port})
		if err == nil {
			break
		}
	}
	if port == 65535 {
		log.Fatal("No available ports!")
	}
	err = exec.Command(ipfsPath, "p2p", "listen", protocol, "/ipv4/127.0.0.1/tcp/"+strconv.Itoa(port)).Run()
	if err != nil {
		c.writePortMessage(smsgListenRefused, port)
		lisnr.Close()
	} else {
		lis := createListener()
		lis.port = port
		lis.listener = lisnr
		c.listeners[protocol] = lis
		lis.run(c)
	}
}

// STOP LISTENER API METHOD
func (c *client) stopListener(protocol string) {
	listener := c.listeners[protocol]
	if listener != nil {
		listener.close()
	}
}

// CLOSE STREAM API METHOD
func (c *client) closeStream(id uint64) {
	lis := c.listenerConnections[id]
	if lis != nil {
		lis.closeConnection(id)
	}
	fwd := c.forwarders[id]
	if fwd != nil {
		fwd.close()
	}
}

// SEND DATA API METHOD
func (c *client) sendData(id uint64, data []byte) {
	var con *net.TCPConn
	lis := c.listenerConnections[id]
	if lis != nil {
		con = lis.connections[id]
	}
	fwd := c.forwarders[id]
	if fwd != nil {
		con = fwd.connection
	}
	remaining := data[:]
	for len(remaining) > 0 {
		len, serr := con.Write(remaining)
		if serr != nil {
			c.control.WriteMessage(websocket.CloseMessage, make([]byte, 0))
			log.Printf("error: %v\n", serr)
			break
		}
		remaining = remaining[len:]
	}
}

// CONNECT API METHOD
func (c *client) connect(protocol string, peerid string) {
	var con *net.TCPConn
	port := choosePort()
	err := exec.Command(ipfsPath, "p2p", "forward", protocol, "/ipv4/127.0.0.1/tcp/"+strconv.Itoa(port), peerid).Run()
	if err == nil {
		con, err = net.DialTCP("tcp4", &net.TCPAddr{Port: port}, nil)
	}
	if err != nil {
		c.writeMessage(smsgPeerConnectionRefused, []byte(fmt.Sprintf("Could not create connection to %v", peerid)))
	} else {
		id := c.newConnectionID()
		fwd := new(forwarder)
		*fwd = forwarder{port, id,con, c}
		c.forwarders[id] = fwd
		c.writeIDMessage(smsgPeerConnection, id)
		c.readSocket(id, con)
	}
}

func (c *client) newConnectionID() uint64 {
	id := c.nextConnectionID
	c.nextConnectionID++
	return id
}

func choosePort() int {
	for port := 10000; port < 65536; port++ {
		listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IP{127,0,0,1}, Port: port})
		if err == nil {
			listener.Close()
			return port
		}
	}
	log.Fatal("No available ports!")
	return -1
}

func getString(data []byte) (string, []byte) {
	len := binary.BigEndian.Uint16(data)
	return string(data[2:2+len]), data[2+len:]
}

func (c *client) writeIDMessage(typ messageType, id uint64) error {
	msg := [9]byte{byte(typ)}
	binary.BigEndian.PutUint64(msg[1:], id)
	return c.writeFullMessage(msg[:])
}

func (c *client) writePortMessage(typ messageType, port int) error {
	msg := [5]byte{byte(typ)}
	binary.BigEndian.PutUint32(msg[1:], uint32(port))
	return c.writeFullMessage(msg[:])
}

func (c *client) writeMessage(typ messageType, body []byte) error {
	msg := make([]byte, len(body) + 1)
	msg[0] = byte(typ)
	copy(msg[1:], body)
	return c.writeFullMessage(msg)
}

func (c *client) writeFullMessage(msg []byte) error {
	werr := c.control.WriteMessage(websocket.BinaryMessage, msg)
	if werr != nil {
		c.close()
	}
	return werr
}

func createRelay() *relay {
	r := new(relay)
	r.clients = make(map[*websocket.Conn]*client)
	r.managementChan = make(chan func())
	return r
}

func (r *relay) getSvcChannel() chan func() {
	return r.managementChan
}

func (r *relay) handleConnection() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		upgrader := websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		}
		con, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			log.Printf("error: %v", err)
		} else {
			svc(r, func() {
				client := createClient()
				client.control = con
				r.clients[con] = client
				client.writeMessage(smsgIdent, []byte(peerId))
				runSvc(client)
				client.readWebsocket()
			})
		}
	}
}

func runIpfsCmd(args ...string) string {
    cmd := exec.Command(ipfsPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("Error running ipfs %#v\n%v\n", args, err)
	}
	return string(output)
}

func main() {
	path, err := exec.LookPath("ipfs")
	if err != nil {
		log.Fatal(err)
	}
	ipfsPath = path
	peerId = strings.Trim(runIpfsCmd("id", "-f", "<id>\n"), " \n")
	relay := createRelay()
	runSvc(relay)
	port := 8888
	files := ""
	flag.StringVar(&files, "files", "", "optional directory to use for file serving")
	flag.IntVar(&port, "port", 8888, "port to listen on")
	flag.Parse()
	fmt.Printf("Peer id: %v\nListening on port %v\n", peerId, port)
	http.HandleFunc("/ipfswsrelay", relay.handleConnection())
	if files != "" {
		f, err := filepath.Abs(files)
		if err != nil {
			log.Fatal(err)
		}
		files = f
		if files != "" {
			http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				reqFile, err := filepath.Abs(filepath.Join(files, r.URL.Path))
				if err != nil || len(reqFile) < len(files) || files[:] != reqFile[0:len(files)] {
					http.Error(w, "Not found", http.StatusNotFound)
				} else {
					http.ServeFile(w, r, reqFile)
				}
			})
		}
	}
	log.Fatal(http.ListenAndServe(fmt.Sprintf("localhost:%d", port), nil))
}
