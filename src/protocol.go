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
  Listen:  [1][FRAMES: 1][PROTOCOL: rest] -- request a listener for a protocol (frames optional)
  Stop:    [2][PROTOCOL: rest]            -- stop listening on PORT
  Close:   [3][ID: 8]                     -- close a stream
  Data:    [4][ID: 8][data: rest]         -- write data to stream
  Connect: [5][FRAMES: 1][PROTOCOL: STR][PEERID: rest] -- connect to another peer (frames optional)
```

# SERVER-TO-CLIENT MESSAGES

```
  Listener Connection:     [1][ID: 8][PROTOCOL: rest] -- new listener connection with id ID
  Connection Closed:       [2][ID: 8]                 -- connection ID closed
  Data:                    [3][ID: 8][data: rest]     -- receive data from stream with id ID
  Listen Refused:          [4][PROTOCOL: rest]        -- could not listen on PORT
  Listener Closed:         [5][PROTOCOL: rest]        -- could not listen on PORT
  Peer Connection:         [6][ID: 8][PROTOCOL: rest] -- connected to a peer with id ID
  Peer Connection Refused: [7][PEERID: rest]          -- connection to peer PEERID refused
  Protocol Error:          [8][MSG: rest]             -- error in the protocol
```

This code uses quite a few goroutines and channels. Here is the pattern:

1) structs which implement the chanSvc interface use a channel to receive functions to execute within a single goroutine

2) fields commented with "immutable" are safe to read in any goroutine

3) in general, methods are only safe to use within their own goroutines

4) methods marked SVC are safe to call from outside because they wrap their code in a svc() call

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
	"runtime/debug"
	"math/rand"
	"time"
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
	smsgError
)

var cmsgNames = [...]string{"cmsgListen", "cmsgStop", "cmsgClose", "cmsgData", "cmsgConnect"}
var smsgNames = [...]string{"smsgIdent", "smsgNewConnection", "smsgConnectionClosed", "smsgData", "smsgListenRefused", "smsgListenerClosed", "smsgPeerConnection", "smsgPeerConnectionRefused", "smsgError"}

const (
	ipfsName = "ipfs"
	maxMessageSize = 65536 // Maximum websocket message size
	minPort = 49152
	maxPort = 65535
)

var ipfsPath string
var peerId string
var frameLength = make([]byte, 4)

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
	connections map[uint64]*connection     // connectionID -> connection
	port int
	protocol string
	frames bool                            // whether to transmit frame lengths
	managementChan chan func()                  // client management
}

type connection struct {
	id uint64                              // immutable
	tcpCon *net.TCPConn                    // immutable
	client *client                         // immutable
	frames bool                            // immutable
	writeChan chan func()                  // client management
	transferChan chan bool
	readBuf []byte
	writeBuf []byte
	name string
	protocol string
}

type forwarder struct {
	connection
	port int                       // a record of the forwarder's port so we can close the IPFS forwarding service
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
	buf []byte
	transferChan chan bool
}

type relay struct {
	clients map[*websocket.Conn]*client    // id -> client
	managementChan chan func()             // client creation
}

type chanSvc interface {
	getSvcChannel() chan func()
}

func (t messageType) clientName() string {
	return cmsgNames[t]
}

func (t messageType) serverName() string {
	return smsgNames[t]
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

func stringFor(t *net.TCPConn) string {
	return t.LocalAddr().String() + " -> " + t.RemoteAddr().String()
}

func createConnection(protocol string, conID uint64, con *net.TCPConn, client *client, frames bool) *connection {
	tcpCon := new(connection)
	tcpCon.initConnection("connection", protocol, conID, con, client, frames)
	return tcpCon
}

func (c *connection) String() string {
	return c.name + "("+c.protocol+", "+stringFor(c.tcpCon)+")"
}

func (c *connection) getSvcChannel() chan func() {
	return c.writeChan
}

func (c *connection) initConnection(name string, protocol string, conID uint64, con *net.TCPConn, client *client, frames bool) {
	*c = connection{conID, con, client, frames, make(chan func()), make(chan bool), make([]byte, maxMessageSize), make([]byte, maxMessageSize), name, protocol}
	runSvc(c)
}

func (c *connection) readFully(buf []byte) error {
	var remaining = len(buf)

	for remaining > 0 {
		c.tcpCon.SetReadDeadline(time.Now().Add(10 * time.Second))
		bytes, err := c.tcpCon.Read(buf)
		if err != nil {
			_, ok := err.(*net.OpError)
			if ok {
				fmt.Println("continuing from read timeout")
				continue
			}
			fmt.Println("ERROR IN", c, ":", err.Error())
			debug.PrintStack()
			return err
		}
		fmt.Println("read ", bytes, " bytes")
		if bytes > 0 {
			buf = buf[bytes:]
			remaining -= bytes
		}
	}
	return nil
}

func createForwarder(protocol string, port int, conID uint64, con *net.TCPConn, client *client, frames bool) *forwarder {
	fwd := new(forwarder)
	fwd.initConnection("forwarder", protocol, conID, con, client, frames)
	fwd.port = port
	client.forwarders[conID] = fwd
	return fwd
}

func (c *connection) writeData(data []byte) {
	for len(data) > 0 {
		c.tcpCon.SetWriteDeadline(time.Now().Add(10 * time.Second))
		len, err := c.tcpCon.Write(data)
		if err != nil {
			_, ok := err.(*net.OpError)
			if ok {
				fmt.Println("continuing from write timeout")
				continue
			}
			svc(c.client, func() {
				c.client.control.WriteMessage(websocket.CloseMessage, make([]byte, 0))
				log.Printf("error: %v\n", err)
				c.client.closeStream(c.id)
			})
			break
		}
		data = data[len:]
	}
}

func (c *connection) close(then func()) {
	svc(c, func() {
		if c.tcpCon != nil {
			c.tcpCon.Close()
			c.tcpCon = nil
			then()
		}
	})
}

func (f *forwarder) close() {
	f.connection.close(func() {
		svc(f.client, func() {
			delete(f.client.forwarders, f.id)
		})
	})
}

func createListener() *listener {
	l := new(listener)
	l.connections = make(map[uint64]*connection)
	l.managementChan = make(chan func())
	return l
}

func (l *listener) getSvcChannel() chan func() {
	return l.managementChan
}


// process connections as they come in
func (l *listener) run(c *client) {
	go func() {
		var tcpCon *net.TCPConn
		var err error = nil

		for err != nil {
			tcpCon, err = l.listener.AcceptTCP()
			svc(c, func() {
				if err != nil {
					l.close()
				} else {
					conID := c.newConnectionID()
					con := createConnection(l.protocol, conID, tcpCon, c, l.frames)
					fmt.Println("CONNECTION: ", con)
					l.connections[conID] = con
					c.listenerConnections[conID] = l
					c.read(con)
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
	cmd(ipfsPath, "p2p", "close", "-l", strconv.Itoa(l.port)).Run()
	l.client.writeMessage(smsgListenerClosed, []byte(l.protocol))
	delete(l.client.listeners, l.protocol)
}

func (l *listener) closeConnection(id uint64) {
	fmt.Println("CLOSING SERVICE CONNECTION ", id)
	delete(l.connections, id)
	l.connections[id].close(func() {
		svc(l.client, func() {
			delete(l.client.listenerConnections, id)
		})
	})
}

func createClient() *client {
	c := new(client)
	c.listeners = make(map[string]*listener)
	c.listenerConnections = make(map[uint64]*listener)
	c.forwarders = make(map[uint64]*forwarder)
	c.managementChan = make(chan func())
	c.buf = make([]byte, maxMessageSize)
	c.transferChan = make(chan bool)
	return c
}

func (c *client) getSvcChannel() chan func() {
	return c.managementChan
}

func (c *client) hasConnection(conID uint64) bool {
	return c.listenerConnections[conID] != nil || c.forwarders[conID] != nil
}

// write a frame to the client
func (c *client) receiveFrameSVC(con *connection, buf []byte, err error) {
	svc(c, func() {
		if err != nil {
			fmt.Println("Error reading data from", con, ": ", err.Error())
			c.closeStreamWithMessage(con.id, err.Error())
		} else {
			c.writeFullMessage(buf)
		}
		con.transferChan <- true
	})
	<- con.transferChan // wait for client to write the buffer out
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

func (c *client) assertConnection(conID uint64, test bool, msg string) bool {
	if !test {
		c.closeStreamWithMessage(conID, msg)
	}
	return test
}

func (c *client) assert(test bool, msg string) bool {
	if !test {
		fmt.Println("ERROR: ", msg)
		c.writeMessage(smsgError, []byte(msg))
	}
	return test
}

func (c *client) readWebsocket() {
	go func() {
		_, data, err := c.control.ReadMessage()
		svc(c, func() {
			if err == nil && len(data) > 0 {
				fmt.Printf("MSG TYPE: %v\n", messageType(data[0]).clientName())
				switch messageType(data[0]) {
				case cmsgListen:
					if c.assert(len(data) > 2, "Bad message format for cmsgListen") {
						c.listen(string(data[2:]), data[1] != 0)
					}
				case cmsgStop:
					if c.assert(len(data) > 1, "Bad message format for cmsgStop") {
						c.stopListener(string(data[1:]))
					}
				case cmsgClose:
					if c.assert(len(data) == 9, "Bad message format for cmsgClose") {
						c.closeStream(binary.BigEndian.Uint64(data[1:9]))
					}
				case cmsgData:
					if c.assert(len(data) > 9, "Bad message format for cmsgData") {
						c.sendData(binary.BigEndian.Uint64(data[1:9]), data[9:])
					}
				case cmsgConnect:
					if c.assert(len(data) > 4, "Bad message format for cmsgConnect") {
						prot, peerid := getString(data[2:])
						fmt.Println("Prot: "+prot+", Peer id: "+string(peerid))
						c.connect(prot, string(peerid), data[1] != 0)
					}
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

func cmd(prog string, args ...string) *exec.Cmd {
	str := [...]interface{}{"COMMAND: ", prog}
	iargs := make([]interface{}, len(args))
	for k, v := range args {
		iargs[k] = v
	}
	fmt.Println(append(str[:], iargs...)...)
	return exec.Command(ipfsPath, args...)
}

// LISTEN API METHOD
func (c *client) listen(protocol string, frames bool) {
	var port int
	var lisnr *net.TCPListener
	var err error

	attempts := 0
	for {
		port = (rand.Int() % (maxPort - minPort)) + minPort
		lisnr, err = net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IP{127,0,0,1}, Port: port})
		if err == nil {
			break
		}
		attempts++
		if attempts > 1000 {
			log.Fatal("No available ports!")
		}
	}
	err = cmd(ipfsPath, "p2p", "listen", protocol, "/ip4/127.0.0.1/tcp/"+strconv.Itoa(port)).Run()
	if err != nil {
		fmt.Println(err.Error())
		c.writePortMessage(smsgListenRefused, port)
		lisnr.Close()
	} else {
		lis := createListener()
		lis.frames = frames
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

func (c *client) putId(conID uint64, offset int) {
	binary.BigEndian.PutUint64(c.buf[offset:], conID)
}

func (c *client) closeStreamWithMessage(conID uint64, msg string) {
	buf := make([]byte, 9 + len(msg))
	buf[0] = byte(smsgConnectionClosed)
	binary.BigEndian.PutUint64(buf[1:], conID)
	c.writeFullMessage(buf[:])
	c.closeStream(conID)
}

// SEND DATA API METHOD
func (c *client) sendData(id uint64, data []byte) {
	var con *connection
	var frames bool
	lis := c.listenerConnections[id]
	if lis != nil {
		con = lis.connections[id]
	}
	fwd := c.forwarders[id]
	if fwd != nil {
		con = &fwd.connection
	}
	svc(con, func() {
		offset := 0
		if frames {
			binary.BigEndian.PutUint32(con.writeBuf, uint32(len(data)))
			offset = 4 // bytes for uint32
		}
		copy(con.writeBuf[offset:], data)
		c.transferChan <- true // done with data
		con.writeData(con.writeBuf[0:len(data) + offset])
	})
	<- c.transferChan // wait until con is done with data
}

// CONNECT API METHOD
func (c *client) connect(protocol string, peerid string, frames bool) {
	var con *net.TCPConn

	port := choosePort()
	err := cmd(ipfsPath, "p2p", "forward", protocol, "/ip4/127.0.0.1/tcp/"+strconv.Itoa(port), "/p2p/"+peerid).Run()
	if err == nil {
		con, err = net.DialTCP("tcp4", &net.TCPAddr{}, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	}
	if err != nil {
		msgLen := 1 + len(protocol) + 2 + len(peerid) + 2 + len(err.Error())
		c.buf[0] = byte(smsgPeerConnectionRefused)
		next := putString(c.buf[1:], protocol)
		next = putString(next, peerid)
		copy(next, err.Error())
		fmt.Println("ERROR: ", err.Error())
		c.writeFullMessage(c.buf[:msgLen])
	} else {
		id := c.newConnectionID()
		fwd := createForwarder(protocol, port, id, con, c, frames)
		c.forwarders[id] = fwd
		c.read(&fwd.connection)
		c.buf[0] = byte(smsgPeerConnection)
		c.putId(id, 1)
		copy(c.buf[9:], protocol)
		c.writeFullMessage(c.buf[:9 + len(protocol)])
	}
}

func (c *client) read(con *connection) {
	con.tcpCon.SetReadBuffer(maxMessageSize)
	con.tcpCon.SetWriteBuffer(maxMessageSize)
	con.readBuf[0] = byte(smsgData)
	binary.BigEndian.PutUint64(con.readBuf[1:], con.id)
	if con.frames {
		c.readSocketFrames(con)
	} else {
		c.readSocketData(con)
	}
}

func (c *client) readSocketFrames(con *connection) {
	if c.hasConnection(con.id) {
		go func() {
			body := con.readBuf[9:]
			lenbuf := con.readBuf[9:14]
			for i, _ := range lenbuf {lenbuf[i] = 32}
			err := con.readFully(lenbuf) // read the length
			if err == nil {
				len := binary.BigEndian.Uint32(lenbuf)
				fmt.Printf("RECEIVED %d BYTES, %v\n", len, lenbuf)
				err = con.readFully(body[:len])
			}
			svc(c, func() {
				c.receiveFrameSVC(con, con.readBuf, err)
				c.readSocketFrames(con) // read again in another go routine
			})
		}()
	}
}

func (c *client) readSocketData(con *connection) {
	if c.hasConnection(con.id) {
		go func() {
			body := con.readBuf[1:]
			len, err := con.tcpCon.Read(body)
			svc(c, func() {
				if err != nil {
					c.receiveFrameSVC(con, con.readBuf, err)
				} else {
					fmt.Println("RECEIVED ", len, " BYTES")
					c.receiveFrameSVC(con, con.readBuf[0:1 + len], err)
				}
				c.readSocketData(con);
			})
		}()
	}
}

func (c *client) newConnectionID() uint64 {
	id := c.nextConnectionID
	c.nextConnectionID++
	return id
}

func choosePort() int {
	attempts := 0

	//The Internet Assigned Numbers Authority (IANA) suggests the range 49152 to 65535 for dynamic or private ports
	for {
		port := (rand.Int() % (maxPort - minPort)) + minPort
		listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IP{127,0,0,1}, Port: port})
		if err == nil {
			listener.Close()
			return port
		}
		attempts++
		if attempts > 1000 {
			log.Fatal("No available ports!")
		}
	}
	return -1
}

func getString(data []byte) (string, []byte) {
	len := binary.BigEndian.Uint16(data)
	return string(data[2:2+len]), data[2+len:]
}

func putString(buf []byte, str string) []byte {
	binary.BigEndian.PutUint16(buf, uint16(len(str)))
	copy(buf[2:], str)
	return buf[2 + len(str):]
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
	fmt.Printf("Writing message type %v\n", messageType(msg[0]).serverName())
	//debug.PrintStack()
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
		//con.SetReadDeadline(time.Unix(0, 0))
		//con.SetWriteDeadline(time.Unix(0, 0))
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
    cmd := cmd(ipfsPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("Error running ipfs %#v\n%v\n", args, err)
	}
	return string(output)
}
