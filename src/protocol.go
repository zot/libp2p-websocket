/* Copyright (c) 2020, William R. Burdick Jr.
 *
 * The MIT License (MIT)
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
 * THE SOFTWARE.
 *
 */

package main

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
  Listen:      [0][FRAMES: 1][PROTOCOL: rest] -- request a listener for a protocol (frames optional)
  Stop:        [1][PROTOCOL: rest]            -- stop listening on PORT
  Close:       [2][ID: 8]                     -- close a stream
  Data:        [3][ID: 8][data: rest]         -- write data to stream
  Connect:     [4][FRAMES: 1][PROTOCOL: STR][PEERID: rest] -- connect to another peer (frames optional)
  Dsc Listen:  [5][FRAMES: 1][PROTOCOL: rest] -- host a protocol using discovery
  Dsc Connect: [6][FRAMES: 1][PROTOCOL: rest] -- request a connection to a protocol using discovery
```

# SERVER-TO-CLIENT MESSAGES

```
  Identify:                [0][PEERID: str]                    -- successful initialization
  Listener Connection:     [1][ID: 8][PEERID: str][PROTOCOL: rest] -- new listener connection with id ID
  Connection Closed:       [2][ID: 8]                          -- connection ID closed
  Data:                    [3][ID: 8][data: rest]              -- receive data from stream with id ID
  Listen Refused:          [4][PROTOCOL: rest]                 -- could not listen on PORT
  Listener Closed:         [5][PROTOCOL: rest]                 -- could not listen on PORT
  Peer Connection:         [6][ID: 8][PROTOCOL: rest]          -- connected to a peer with id ID
  Peer Connection Refused: [7][PEERID: string][PROTOCOL: rest] -- connection to peer PEERID refused
  Protocol Error:          [8][MSG: rest]                      -- error in the protocol
  Dsc Listener Connection: [9][ID: 8][PEERID: str][PROTOCOL: rest] -- connection from a discovery peer
  Dsc Peer Connection:     [10][ID: 8][PEERID: str][PROTOCOL: rest] -- connected to a discovery host
```

This code uses quite a few goroutines and channels. Here is the pattern:

1) structs which implement the chanSvc interface use a channel to receive functions to execute within a single svc goroutine

2) fields commented with "immutable" don't change, so they are safe to read in any goroutine

3) in general, methods are only safe to use within their own svc goroutines

*/

import (
	"encoding/binary"
	"fmt"
	"github.com/gorilla/websocket"
	"log"
	"net"
	"net/http"
	//"runtime/debug"
	"math/rand"
	"time"
	"io"
	"errors"
	"syscall"
	"bytes"
)

type messageType byte
const (
	cmsgListen messageType = iota
	cmsgStop
	cmsgClose
	cmsgData
	cmsgConnect
	cmsgDscListen
	cmsgDscConnect
	smsgIdent = iota - cmsgDscConnect - 1 // restart at 0
	smsgNewConnection
	smsgConnectionClosed
	smsgData
	smsgListenRefused
	smsgListenerClosed
	smsgPeerConnection
	smsgPeerConnectionRefused
	smsgError
	smsgDscHostConnect
	smsgDscPeerConnect
)

var cmsgNames = [...]string{"cmsgListen", "cmsgStop", "cmsgClose", "cmsgData", "cmsgConnect", "cmsgDscListen", "cmsgDscConnect"}
var smsgNames = [...]string{"smsgIdent", "smsgNewConnection", "smsgConnectionClosed", "smsgData", "smsgListenRefused", "smsgListenerClosed", "smsgPeerConnection", "smsgPeerConnectionRefused", "smsgError", "smsgDscHostConnect", "smsgDscPeerConnect"}

const (
	maxMessageSize = 65536 // Maximum websocket message size
	minPort = 49152
	maxPort = 65535
)

var frameLength = make([]byte, 4)

var (
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
)

type twoWayStream interface {
	io.Reader
	io.Writer
	io.Closer
	SetDeadline(time.Time) error
    SetReadDeadline(time.Time) error
    SetWriteDeadline(time.Time) error
}

type connection struct {
	id uint64                                // immutable
	stream twoWayStream                      // immutable
	client *client                           // immutable
	frames bool                              // immutable
	writeChan chan func()                    // client management
	transferChan chan bool
	readBuf []byte
	writeBuf []byte
	name string
	protocol string
}

// client allows a browser to use the relay
type client struct {
	nextConnectionID uint64
	control *websocket.Conn                  // the client's control websocket
	managementChan chan func()               // client management
	running bool
	buf []byte
	transferChan chan bool
	relay *relay
}

type relay struct {
	clients map[*websocket.Conn]*client      // id -> client
	managementChan chan func()               // client creation
	handler protocolHandler
	peerID string
}

type protocolHandler interface {
	HasConnection(c *client, id uint64) bool
	CreateClient() *client
	Listen(c *client, protocol string, frames bool)
	Stop(c *client, protocol string)
	Close(c *client, conID uint64)
	Data(c *client, conID uint64, data []byte)
	Connect(c *client, protocol string, peerID string, frames bool)
}

/*
 * A callback service that uses the following advertisement strings:
 *   libp2p-connection-direct-PROTOCOL: this peer is hosting PROTOCOL and can receive direct connections
 *   libp2p-connection-indirect-PROTOCOL: this peer is hosting PROTOCOL but can only make outgoing connections. It is expected to monitor connection requests and also use the circuit relay
 *   libp2p-connection-request-PROTOCOL: this peer is requesting a connection for PROTOCOL
 */
type discoveryHandler interface {
	DiscoveryListen(c *client, frames bool, protocol string) error
	DiscoveryConnect(c *client, frames bool, protocol string) error
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

func stringFor(stream twoWayStream) string {
	if t, ok := stream.(*net.TCPConn); ok {
		return t.LocalAddr().String() + " -> " + t.RemoteAddr().String()
	} else if t, ok := stream.(interface {String() string}); ok {
		return t.String()
	} else {
		return "a connection"
	}
}

func createConnection(protocol string, conID uint64, stream twoWayStream, client *client, frames bool) *connection {
	con := new(connection)
	con.initConnection("connection", protocol, conID, stream, client, frames)
	return con
}

func (c *connection) String() string {
	return c.name + "("+c.protocol+", "+stringFor(c.stream)+")"
}

func (c *connection) getSvcChannel() chan func() {
	fmt.Println("GETTING SVC CHANNEL FOR", c)
	return c.writeChan
}

func (c *connection) initConnection(name string, protocol string, conID uint64, con twoWayStream, client *client, frames bool) {
	*c = connection{
		conID,
		con,
		client,
		frames,
		make(chan func()),
		make(chan bool),
		make([]byte, maxMessageSize),
		make([]byte, maxMessageSize),
		name,
		protocol,
	}
	runSvc(c)
}

func (c *connection) writeData(r *relay, data []byte) {
	svc(c, func() {
		fmt.Println("START WRITING DATA")
		offset := 0
		if c.frames {
			binary.BigEndian.PutUint32(c.writeBuf, uint32(len(data)))
			offset = 4 // bytes for uint32
		}
		copy(c.writeBuf[offset:], data)
		c.transferChan <- true // done with data
		data = c.writeBuf[0:len(data) + offset]
		for len(data) > 0 {
			c.stream.SetWriteDeadline(time.Unix(0, 0))
			len, err := c.stream.Write(data)
			if err != nil {
				if _, ok := err.(net.Error); ok && err.(net.Error).Timeout() {
					fmt.Println("continuing from write timeout")
					continue
				}
				if err != nil && errors.Is(err, syscall.ETIMEDOUT) {
					fmt.Println("continuing from write timeout")
					continue
				}
				if err != nil {
					fmt.Println("ERROR WRITING DATA TO STREAM", err)
				}
				svc(c.client, func() {
					c.client.control.WriteMessage(websocket.CloseMessage, make([]byte, 0))
					log.Printf("error: %v\n", err)
					r.Close(c.client, c.id)
				})
				break
			}
			data = data[len:]
		}
		fmt.Println("FINISHED WRITING DATA")
	})
	<- c.transferChan // wait until done transferring data
}

func (c *connection) close(then func()) {
	svc(c, func() {
		if c.stream != nil {
			c.stream.Close()
			then()
		}
	})
}

func (c *client) init(r *relay) {
	c.managementChan = make(chan func())
	c.buf = make([]byte, maxMessageSize)
	c.transferChan = make(chan bool)
	c.relay = r
}

func (c *client) getSvcChannel() chan func() {
	return c.managementChan
}

// write a frame to the client
func (c *client) receiveFrame(con *connection, buf []byte, err error) {
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
		c.writePackedMessage(smsgError, msg)
	}
	return test
}

func (c *client) readWebsocket(r *relay) {
	fmt.Printf("READING WEB SOCKET")
	go func() {
		var err error = nil
		var data []byte
		syncChan := make(chan bool)

		for err == nil {
			fmt.Println("WAITING FOR CLIENT MESSAGE")
			c.control.SetReadDeadline(time.Now().Add(60 * time.Second))
			_, data, err = c.control.ReadMessage()
			fmt.Println("DONE READING CLIENT MESSAGE")
			if _, ok := err.(net.Error); ok && err.(net.Error).Timeout() {
				fmt.Println("CONTINUING WEB SOCKET READ AFTER TIMEOUT")
				continue
			}
			if err != nil && errors.Is(err, syscall.ETIMEDOUT) {
				fmt.Println("CONTINUING WEB SOCKET READ AFTER TIMEOUT")
				continue
			}
			if err == nil {
				fmt.Println("READ MESSAGE", messageType(data[0]).clientName())
			} else {
				fmt.Println("ERROR READING WEB SOCKET", err)
			}
			svc(c, func() {
				if err == nil && len(data) > 0 {
					fmt.Printf("MSG TYPE: %v\n", messageType(data[0]).clientName())
					body := data[1:]
					switch messageType(data[0]) {
					case cmsgListen:
						if c.assert(len(body) > 1, "Bad message format for cmsgListen") {
							r.Listen(c, string(body[1:]), body[0] != 0)
						}
					case cmsgStop:
						if c.assert(len(body) > 0, "Bad message format for cmsgStop") {
							r.Stop(c, string(body))
						}
					case cmsgClose:
						if c.assert(len(body) == 8, "Bad message format for cmsgClose") {
							r.Close(c, binary.BigEndian.Uint64(body[:8]))
						}
					case cmsgData:
						if c.assert(len(body) > 8, "Bad message format for cmsgData") {
							r.Data(c, binary.BigEndian.Uint64(body[:8]), body[8:])
						}
					case cmsgConnect:
						if c.assert(len(body) > 3, "Bad message format for cmsgConnect") {
							prot, peerid := getString(body[1:])
							fmt.Println("Prot: "+prot+", Peer id: "+string(peerid))
							r.Connect(c, prot, string(peerid), body[0] != 0)
						}
					case cmsgDscListen:
						if c.assert(len(body) > 2, "Bad message formag for cmsgDscListen") {
							r.DiscoveryListen(c, body[0] != 0, string(body[1:]))
						}
					case cmsgDscConnect:
						if c.assert(len(body) > 2, "Bad message formag for cmsgDscConnect") {
							r.DiscoveryConnect(c, body[0] != 0, string(body[1:]))
						}
					}
				} else if err != nil {
					log.Printf("error: %v\n", err)
					c.close()
				}
				syncChan <- true
			})
			<-syncChan
		}
		fmt.Println("DONE READING WEB SOCKET")
	}()
}

func (c *client) putID(conID uint64, offset int) {
	binary.BigEndian.PutUint64(c.buf[offset:], conID)
}

func (c *client) closeStreamWithMessage(conID uint64, msg string) {
	c.writePackedMessage(smsgConnectionClosed, conID)
	c.relay.Close(c, conID)
}

func (c *client) read(con *connection) {
	con.readBuf[0] = byte(smsgData)
	binary.BigEndian.PutUint64(con.readBuf[1:], con.id)
	if con.frames {
		c.readStreamFrames(con)
	} else {
		c.readStreamData(con)
	}
}

func (c *client) readStreamFrames(con *connection) {
	fmt.Println("READING CONNECTION:", con.id)
	go func() {
		var err error = nil

		for err == nil {
			body := con.readBuf[9:]
			lenbuf := con.readBuf[9:13]
			for i := range lenbuf {lenbuf[i] = 32}
			con.stream.SetReadDeadline(time.Now().Add(60 * time.Second))
			_, err = io.ReadFull(con.stream, lenbuf) // read the length
			if err == nil {
				len := binary.BigEndian.Uint32(lenbuf)
				fmt.Printf("RECEIVING %d BYTES, %v\n", len, lenbuf)
				_, err = io.ReadFull(con.stream, body[:len])
				c.receiveFrame(con, con.readBuf[:len + 9], err)
			} else {
				fmt.Printf("ERROR READING FRAME LENGTH: %v\n", err)
			}
		}
	}()
}

func (c *client) readStreamData(con *connection) {
	go func() {
		var err error

		for err == nil {
			body := con.readBuf[1:]
			len, err := con.stream.Read(body)
			if err != nil {
				c.receiveFrame(con, con.readBuf, err)
			} else {
				fmt.Println("RECEIVED ", len, " BYTES")
				c.receiveFrame(con, con.readBuf[0:1 + len], err)
			}
		}
	}()
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

/*
 * pack data into a message, handling these types:
 *   string
 *   []byte
 *   byte
 *   uint32
 *   uint64
 */
func pack(typ messageType, items ...interface{}) []byte {
	buf := bytes.NewBuffer(make([]byte, 0, 16))
	buf.WriteByte(byte(typ))
	for i, item := range items {
		switch item.(type) {
		case byte:
			buf.WriteByte(item.(byte))
		case []byte:
			buf.Write(item.([]byte))
		case string:
			str := item.(string)
			if i + 1 < len(items) {
				binary.Write(buf, binary.BigEndian, uint16(len(str)))
			}
			buf.Write([]byte(str))
		case uint32:
			binary.Write(buf, binary.BigEndian, item.(uint32))
		case uint64:
			binary.Write(buf, binary.BigEndian, item.(uint64))
		}
	}
	fmt.Printf("PACKED BYTES: %#v\n", buf.Bytes())
	return buf.Bytes()
}

func (c *client) writeMessage(typ messageType, body []byte) error {
	return c.writePackedMessage(typ, body)
}

func (c *client) writePackedMessage(typ messageType, items ...interface{}) error {
	fmt.Println(append(array("WRITING", typ.serverName(), "MESSAGE"), items...)...)
	return c.writeFullMessage(pack(typ, items...))
}

func (c *client) writeFullMessage(msg []byte) error {
	fmt.Printf("Writing message type %v\n", messageType(msg[0]).serverName())
	werr := c.control.WriteMessage(websocket.BinaryMessage, msg)
	if werr != nil {
		c.close()
	}
	return werr
}

func (c *client) connectionRefused(err error, peerid string, protocol string) {
	c.writePackedMessage(smsgPeerConnectionRefused, peerid, protocol, err.Error())
}

func (c *client) newConnection(protocol string, create func(conID uint64) *connection) {
	id := c.newConnectionID()
	c.read(create(id))
	c.writePackedMessage(smsgPeerConnection, id, protocol)
}

func (c *client) checkProtocolErr(err error, typ messageType) {
	if err != nil {
		c.writePackedMessage(smsgError, fmt.Sprintf("Bad message format for %v", typ.clientName()))
		c.close()
	}
}

func (r *relay) HasConnection(c *client, id uint64) bool {
	return r.handler.HasConnection(c, id)
}

func (r *relay) CreateClient() *client {
	return r.handler.CreateClient()
}

func (r *relay) Listen(c *client, protocol string, frames bool) {
	r.handler.Listen(c, protocol, frames)
}

func (r *relay) Stop(c *client, protocol string) {
	r.handler.Stop(c, protocol)
}

func (r *relay) Close(c *client, conID uint64) {
	r.handler.Close(c, conID)
}

func (r *relay) Data(c *client, conID uint64, data []byte) {
	r.handler.Data(c, conID, data)
}

func (r *relay) Connect(c *client, protocol string, peerID string, frames bool) {
	r.handler.Connect(c, protocol, peerID, frames)
}

func (r *relay) DiscoveryListen(c *client, frames bool, prot string) {
	dsc, ok := r.handler.(discoveryHandler)
	if !ok {
		c.writePackedMessage(smsgError, "Relay does not support discovery")
	} else {
		dsc.DiscoveryListen(c, frames, prot)
	}
}

func (r *relay) DiscoveryConnect(c *client, frames bool, prot string) {
	dsc, ok := r.handler.(discoveryHandler)
	if !ok {
		c.writePackedMessage(smsgError, "Relay does not support discovery")
	} else {
		dsc.DiscoveryConnect(c, frames, prot)
	}
}

func (r *relay) init() {
	r.clients = make(map[*websocket.Conn]*client)
	r.managementChan = make(chan func())
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
				client := r.CreateClient()
				client.control = con
				r.clients[con] = client
				client.writePackedMessage(smsgIdent, r.peerID)
				runSvc(client)
				client.readWebsocket(r)
			})
		}
	}
}

func newBuf(len int) *bytes.Buffer {
	return bytes.NewBuffer(make([]byte, len))
}

func array(item ...interface{}) []interface{} {
	return item[:]
}
