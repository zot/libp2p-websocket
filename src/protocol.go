/* Copyright (c) 2020, William R. Burdick Jr., Roy Riggs, and TEAM CTHLUHU
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
  Start:       [0][TREEPROTOCOL: str][TREENAME: str][KEY: str][FRIENDS: array of str] -- start peer with optional peer key
  Listen:      [1][FRAMES: 1][PROTOCOL: rest] -- request a listener for a protocol (frames optional)
  Stop:        [2][PROTOCOL: rest] -- stop listening to PROTOCOL
  Close:       [3][ID: 8]                     -- close a stream
  Data:        [4][ID: 8][data: rest]         -- write data to stream
  Connect:     [5][FRAMES: 1][PROTOCOL: STR][RELAY: STR][PEERID: rest] -- connect to another peer (frames optional)
  Friends:     [6][ADD: []str][REMOVE: []str] -- alter friend list
```

# SERVER-TO-CLIENT MESSAGES

```
  Hello:                   [0][STARTED: 1] -- hello message indicates whether the peer needs starting
  Identify:                [1][PUBLIC: 1][PEERID: str][ADDRESSES: str][KEY: rest] -- successful initialization
  Listener Connection:     [2][ID: 8][PEERID: str][PROTOCOL: rest] -- new listener connection with id ID
  Connection Closed:       [3][ID: 8][REASON: rest]            -- connection ID closed
  Data:                    [4][ID: 8][data: rest]              -- receive data from stream with id ID
  Listen Refused:          [5][PROTOCOL: rest]                 -- could not listen on PROTOCOL
  Listener Closed:         [6][PROTOCOL: rest]                 -- could not listen on PROTOCOL
  Peer Connection:         [7][ID: 8][PEERID: str][PROTOCOL: rest] -- connected to a peer with id ID
  Peer Connection Refused: [8][PEERID: str][PROTOCOL: str][ERROR: rest] -- connection to peer PEERID refused
  Protocol Error:          [9][MSG: rest]                      -- error in the protocol
  Listening:               [10][PROTOCOL: rest]                -- confirmation that listening has started
  Access Change:           [11][PUBLIC: 1]                     -- peer access has changed
  Presence Change:         [12][ONLINE: []str][OFFLINE: []str] -- peer access has changed
```

This code uses quite a few goroutines and channels. Here is the pattern:

1) structs which implement the chanSvc interface use a channel to receive functions to execute within a single svc goroutine

2) fields commented with "immutable" don't change, so they are safe to read in any goroutine

3) in general, methods are only safe to use within their own svc goroutines

*/

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"

	"bytes"
	"errors"
	"io"
	"math/rand"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/libp2p/go-libp2p-core/network"
	packet "github.com/zot/textcraft-packet"
)

type messageType byte

const (
	cmsgStart messageType = iota
	cmsgListen
	cmsgStop
	cmsgClose
	cmsgData
	cmsgConnect
	cmsgFriends
)

type cmsgStartParams struct {
	treeProtocol string
	treeName     string
	port         int
	peerKey      string
	friends      []string
}
type cmsgListenStopParams struct {
	boolParam bool
	protocol  string
}
type cmsgCloseParams struct {
	conID string
}
type cmsgDataParams struct {
	conID string
	data  []byte
}
type cmsgConnectParams struct {
	frames bool
	relay  bool
	prot   string
	peerID string
}

type cmsgFriendsParams struct {
	add    []string
	remove []string
}

const (
	smsgHello messageType = iota
	smsgIdent
	smsgListenerConnection
	smsgConnectionClosed
	smsgData
	smsgListenRefused
	smsgListenerClosed
	smsgPeerConnection
	smsgPeerConnectionRefused
	smsgError
	smsgListening
	smsgAccessChange
	smsgPresenceChange
)

type smsgHelloParams struct {
	started bool
	version string
}
type smsgIdentParams struct {
	publicPeer     bool
	hasNat         bool
	peerID         string
	addresses      []string
	peerKey        string
	currentVersion string
}
type smsgListenerConnectionParams struct {
	conID    string
	peerID   string
	protocol string
}
type smsgConnectionClosedParams struct {
	conID  string
	reason string
}
type smsgDataParams struct {
	conID string
	data  []byte
}
type smsgListenRefusedParams struct {
	prot   string
	reason string
}
type smsgListenerClosedParams struct {
	prot string
}
type smsgPeerConnectionParams struct {
	conID    string
	peerID   string
	protocol string
}
type smsgPeerConnectionRefusedParams struct {
	peerID   string
	protocol string
	reaon    string
}
type smsgErrorParams struct {
	message string
}
type smsgListeningParams struct {
	protocol string
}
type smsgAccessChangeParams struct {
	access int
}
type smsgPresenceChangeParams struct {
	online  []string
	offline []string
}

type messageParams interface{ msgType() messageType }

func (smsg smsgHelloParams) msgType() messageType                 { return smsgHello }
func (smsg smsgIdentParams) msgType() messageType                 { return smsgIdent }
func (smsg smsgListenerConnectionParams) msgType() messageType    { return smsgListenerConnection }
func (smsg smsgConnectionClosedParams) msgType() messageType      { return smsgConnectionClosed }
func (smsg smsgDataParams) msgType() messageType                  { return smsgData }
func (smsg smsgListenRefusedParams) msgType() messageType         { return smsgListenRefused }
func (smsg smsgListenerClosedParams) msgType() messageType        { return smsgListenerClosed }
func (smsg smsgPeerConnectionParams) msgType() messageType        { return smsgPeerConnection }
func (smsg smsgPeerConnectionRefusedParams) msgType() messageType { return smsgPeerConnectionRefused }
func (smsg smsgErrorParams) msgType() messageType                 { return smsgError }
func (smsg smsgListeningParams) msgType() messageType             { return smsgListening }
func (smsg smsgAccessChangeParams) msgType() messageType          { return smsgAccessChange }
func (smsg smsgPresenceChangeParams) msgType() messageType        { return smsgPresenceChange }

var cmsgNames = [...]string{"cmsgStart", "cmsgListen", "cmsgStop", "cmsgClose", "cmsgData", "cmsgConnect", "cmsgFriends"}
var smsgNames = [...]string{"smsgHello", "smsgIdent", "smsgNewConnection", "smsgConnectionClosed", "smsgData", "smsgListenRefused", "smsgListenerClosed", "smsgPeerConnection", "smsgPeerConnectionRefused", "smsgError", "smsgListening", "smsgAccessChange", "smsgPresenceChange"}

const (
	maxMessageSize = 65536 // Maximum websocket message size
	minPort        = 49152
	maxPort        = 65535
	pongWait       = 60 * time.Second
	pingPeriod     = pongWait * 9 / 10
	verboseSvc     = false
	//verboseSvc = true
)

var frameLength = []byte{0, 0, 0, 0}
var svcCount int32

var (
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
)

type atomicBoolean int32

func (a *atomicBoolean) Get() bool {
	return atomic.LoadInt32((*int32)(a)) != 0
}

func (a *atomicBoolean) Set(v bool) {
	val := int32(1)
	if !v {
		val = 0
	}
	atomic.StoreInt32((*int32)(a), val)
}

type twoWayStream interface {
	io.Reader
	io.Writer
	io.Closer
	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

type connection struct {
	id           uint64       // immutable
	stream       twoWayStream // immutable
	client       *client      // immutable
	frames       bool         // immutable
	writeChan    chan func()  // client management
	transferChan chan bool
	readBuf      []byte
	writeBuf     []byte
	name         string
	protocol     string
	data         interface{}
}

// client allows a browser to use the relay
type client struct {
	nextConnectionID uint64
	control          *websocket.Conn // the client's control websocket
	managementChan   chan func()     // client management
	running          bool
	buf              []byte
	transferChan     chan bool
	relay            *relay
	data             interface{}
	ticker           *time.Ticker
	access           network.Reachability
}

type relay struct {
	clients        map[*websocket.Conn]*client // id -> client
	managementChan chan func()                 // client creation
	handler        protocolHandler
	peerID         string
	access         network.Reachability
}

type protocolHandler interface {
	Versions() (string, string)
	Started() bool
	Start(treeProtocol string, treeName string, port uint16, peerKey string, friends []string) error
	PeerAccess() chan network.Reachability
	HasConnection(c *client, id uint64) bool
	CreateClient() *client
	StartClient(c *client, init func(public bool, hasNat bool))
	Listen(c *client, protocol string, frames bool)
	Stop(c *client, protocol string, retainConnections bool)
	Close(c *client, conID uint64)
	Data(c *client, conID uint64, data []byte)
	Connect(c *client, protocol string, peerID string, frames bool, relay bool)
	Friends(add []string, remove []string) error
	CleanupClosed(c *connection)
	AddressesJson() string
	AddressArray() []string
	PeerKey() string
	CloseClient(c *client)
}

type chanSvc interface {
	getSvcChannel() chan func()
}

func (t messageType) clientName() string {
	if int(t) < len(cmsgNames) {return cmsgNames[t]}
	return fmt.Sprintf("UNKNOWN SERVER MESSAGE: %d", byte(t))
}

func (t messageType) serverName() string {
	if int(t) < len(smsgNames) {return smsgNames[t]}
	return fmt.Sprintf("UNKNOWN SERVER MESSAGE: %d", byte(t))
}

func svcSync(s chanSvc, code func() interface{}) interface{} {
	result := make(chan interface{})
	svc(s, func() {
		result <- code()
	})
	return <-result
}

func svc(s chanSvc, code func()) {
	go func() { // using a goroutine so the channel won't block
		if verboseSvc {
			count := atomic.AddInt32(&svcCount, 1)
			fmt.Printf("@@ QUEUE SVC %d\n", count)
			s.getSvcChannel() <- func() {
				fmt.Printf("@@ START SVC %d [%d]\n", count, atomic.LoadInt32(&svcCount))
				code()
				fmt.Printf("@@ END SVC %d [%d]\n", count, atomic.LoadInt32(&svcCount))
			}
		} else {
			s.getSvcChannel() <- code
		}
	}()
}

func runSvc(s chanSvc) {
	go func() {
		for {

			cmd, ok := <-s.getSvcChannel()
			if !ok {break}
			cmd()
		}
	}()
}

func stringFor(stream twoWayStream) string {
	if t, ok := stream.(*net.TCPConn); ok {
		return t.LocalAddr().String() + " -> " + t.RemoteAddr().String()
	} else if t, ok := stream.(interface{ String() string }); ok {
		return t.String()
	} else {
		return "a connection"
	}
}

func createConnection(protocol string, conID uint64, stream twoWayStream, client *client, frames bool) *connection {
	fmt.Println("MAKING CONNECTION WITH ID ", conID)
	con := new(connection)
	con.init("connection", protocol, conID, stream, client, frames, nil)
	return con
}

func (c *connection) cleanup() {
	c.client.relay.handler.CleanupClosed(c)
}

func (c *connection) String() string {
	return c.name + "(" + c.protocol + ", " + stringFor(c.stream) + ")"
}

func (c *connection) getSvcChannel() chan func() {
	if verboseSvc {
		log.Output(3, fmt.Sprintf("GETTING SVC CHANNEL FOR %v", c))
	}
	return c.writeChan
}

func (c *connection) init(name string, protocol string, conID uint64, con twoWayStream, client *client, frames bool, data interface{}) {
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
		data,
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
		data = c.writeBuf[0 : len(data)+offset]
		for len(data) > 0 {
			c.stream.SetWriteDeadline(time.Unix(0, 0))
			len, err := c.stream.Write(data)
			if err != nil {
				if _, ok := err.(net.Error); ok && err.(net.Error).Timeout() {
					//fmt.Println("continuing from write timeout")
					continue
				}
				if err != nil && errors.Is(err, syscall.ETIMEDOUT) {
					//fmt.Println("continuing from write timeout")
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
	<-c.transferChan // wait until done transferring data
}

func (c *connection) close(then func()) {
	fmt.Println("CONNECTION CLOSING")
	svc(c, func() {
		if c.stream != nil {
			c.stream.Close()
			c.cleanup()
			then()
		}
	})
}

func (c *client) init(r *relay, data interface{}) {
	c.managementChan = make(chan func())
	c.buf = make([]byte, maxMessageSize)
	c.transferChan = make(chan bool)
	c.relay = r
	c.data = data
	c.running = true
}

func (c *client) getSvcChannel() chan func() {
	return c.managementChan
}

// write a frame to the client
func (c *client) receiveFrame(con *connection, buf []byte, err error) {
	input := make([]byte, len(buf))
	copy(input, buf)
	svc(c, func() {
		if err != nil {
			fmt.Println("Error reading data from", con, "(", con.id, ")", ": ", err.Error())
			c.closeStreamWithMessage(con.id, err.Error())
		} else {
			c.writeMsgpack(&smsgDataParams{strconv.FormatUint(con.id, 10), input})
		}
		con.transferChan <- true
	})
	<-con.transferChan // wait for client to write the buffer out
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
		c.error(msg)
	}
	return test
}

func (c *client) error(msg string) {
	c.writeMsgpack(smsgErrorParams{msg})
}

func (c *client) readWebsocket(r *relay) {
	fmt.Printf("READING WEB SOCKET")
	go func() {
		var err error
		//var data []byte
		var data []uint8
		syncChan := make(chan bool)

		for err == nil {
			fmt.Println("WAITING FOR CLIENT MESSAGE")
			_, data, err = c.control.ReadMessage()
			fmt.Println("DONE READING CLIENT MESSAGE")
			if _, ok := err.(net.Error); ok && err.(net.Error).Timeout() {
				fmt.Println("CONTINUING WEB SOCKET READ AFTER TIMEOUT")
				err = nil
				continue
			}
			if err != nil && errors.Is(err, syscall.ETIMEDOUT) {
				fmt.Println("CONTINUING WEB SOCKET READ AFTER ETIMEDOUT")
				err = nil
				continue
			}
			if err == nil {
				fmt.Printf("@@@ READ MESSAGE %s: %X\n", messageType(data[0]).clientName(), data[1:])
			} else {
				fmt.Println("ERROR READING WEB SOCKET", err)
				break
			}
			svc(c, func() {
				if err == nil && len(data) > 0 {
					msgType := messageType(data[0])
					fmt.Printf("MSG TYPE: %v\n", msgType.clientName())
					switch msgType {
					case cmsgListen, cmsgStop:
						msg := new(cmsgListenStopParams)
						_, err = packet.Unmarshal(data[1:], &msg)
						if c.assert(err == nil && len(msg.protocol) > 0, "Bad message format for cmsgListen") {
							if msgType == cmsgListen {
								r.Listen(c, msg.protocol, msg.boolParam)
							} else {
								r.Stop(c, msg.protocol, msg.boolParam)
							}
						}
					case cmsgClose:
						msg := new(cmsgCloseParams)
						_, err = packet.Unmarshal(data[1:], &msg)
						if c.assert(err == nil, "Bad message format for cmsgClose") {
							id, err := strconv.ParseUint(msg.conID, 10, 64)
							if err == nil {
								r.Close(c, id)
							}
						}
					case cmsgData:
						msg := new(cmsgDataParams)
						_, err = packet.Unmarshal(data[1:], &msg)
						if c.assert(err == nil, "Bad message format for cmsgData") {
							conID, err := strconv.ParseUint(msg.conID, 10, 64)
							if err == nil {
								r.Data(c, conID, msg.data)
							}
						}
					case cmsgConnect:
						msg := new(cmsgConnectParams)
						_, err = packet.Unmarshal(data[1:], &msg)
						if c.assert(err == nil && len(msg.peerID) > 0, "Bad message format for cmsgConnect") {
							fmt.Println("Prot:" + msg.prot + ", Peer id: " + msg.peerID + ", Relay: " + boolString(msg.relay))
							r.Connect(c, msg.prot, msg.peerID, msg.frames, msg.relay)
						}
					case cmsgFriends:
						msg := new(cmsgFriendsParams)
						_, err = packet.Unmarshal(data[1:], &msg)
						if err == nil {
							err = r.Friends(msg.add, msg.remove)
						}
					}
				}
				if err != nil {
					panic(fmt.Sprintf("error: %v\n", err))
				}
				syncChan <- true
			})
			<-syncChan
		}
		fmt.Println("@@@\n@@@ DONE READING WEB SOCKET\n@@@")
		svc(r, func() {
			r.CloseClient(c)
		})
	}()
}

func (c *client) putID(conID uint64, offset int) {
	binary.BigEndian.PutUint64(c.buf[offset:], conID)
}

func (c *client) closeStreamWithMessage(conID uint64, msg string) {
	c.writeMsgpack(&smsgConnectionClosedParams{strconv.FormatUint(conID, 10), msg})
	c.relay.Close(c, conID)
}

func (c *client) read(con *connection) {
	//con.readBuf[0] = byte(smsgData)
	//binary.BigEndian.PutUint64(con.readBuf[1:], con.id)
	if con.frames {
		c.readStreamFrames(con)
	} else {
		c.readStreamData(con)
	}
}

func (c *client) readStreamFrames(con *connection) {
	fmt.Println("READING CONNECTION:", con.id)
	go func() {
		var err error

		for err == nil {
			len := uint32(0)
			//body := con.readBuf[9:]
			body := con.readBuf
			lenbuf := con.readBuf[:4]
			//lenbuf := con.readBuf[9:13]
			err = reallyReadFull(con.stream, lenbuf) // read the length
			if err != nil {
				fmt.Printf("ERROR READING FRAME LENGTH: %v\n", err)
			} else {
				len = binary.BigEndian.Uint32(lenbuf)
				fmt.Printf("RECEIVING %d BYTES, %v\n", len, lenbuf)
				err = reallyReadFull(con.stream, body[:len])
			}
			if err == nil {
				fmt.Printf("RECEIVED %d BYTES: %X\n", len, con.readBuf[:len])
				//fmt.Printf("RECEIVED %d BYTES: %X\n", len, con.readBuf[:9+len])
			}
			c.receiveFrame(con, con.readBuf[:len], err)
			//c.receiveFrame(con, con.readBuf[:len+9], err)
		}
	}()
}

// retry on timeout errors
func reallyReadFull(stream twoWayStream, buf []byte) error {
	start := 0
	for start < len(buf) {
		stream.SetReadDeadline(time.Now().Add(60 * time.Second))
		len, err := io.ReadFull(stream, buf[start:])
		start += len
		if err != nil {
			if _, ok := err.(net.Error); ok && err.(net.Error).Timeout() {
				//fmt.Println("continuing from read timeout")
				continue
			}
			if err != nil && errors.Is(err, syscall.ETIMEDOUT) {
				//fmt.Println("continuing from read timeout")
				continue
			}
			return err
		}
	}
	return nil
}

func (c *client) readStreamData(con *connection) {
	go func() {
		var err error

		for err == nil {
			body := con.readBuf
			//body := con.readBuf[9:]
			len, err := con.stream.Read(body)
			if err != nil {
				c.receiveFrame(con, con.readBuf, err)
				svc(c, func() {
					con.cleanup()
				})
			} else {
				fmt.Printf("RECEIVED %d BYTES: %X\n", len, con.readBuf[0:len])
				c.receiveFrame(con, con.readBuf[0:len], err)
				//fmt.Printf("RECEIVED %d BYTES: %X\n", len, con.readBuf[0:9+len])
				//c.receiveFrame(con, con.readBuf[0:9+len], err)
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
		listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IP{127, 0, 0, 1}, Port: port})
		if err == nil {
			listener.Close()
			return port
		}
		attempts++
		if attempts > 1000 {
			log.Fatal("No available ports!")
		}
	}
}

func getString(data []byte) (string, []byte) {
	len := binary.BigEndian.Uint16(data)
	return string(data[2 : 2+len]), data[2+len:]
}

func putString(buf []byte, str string) []byte {
	binary.BigEndian.PutUint16(buf, uint16(len(str)))
	copy(buf[2:], str)
	return buf[2+len(str):]
}

func (c *client) writeMsgpack(msg messageParams) error {
	return c.closeOnError(func() error {
		return writeMsgpack(c.control, msg)
	})
}

func (c *client) closeOnError(code func() error) error {
	var err error

	func() { // nested here so defer can properly reassign err
		defer func() {
			if x := recover(); x != nil && err == nil {
				if _, ok := x.(error); ok {
					err = x.(error) // this value of err will be available *after* the function exits
				}
			}
		}()

		err = code()
	}()
	if err != nil { // if func's deferred code assigned err, the value will be here
		svc(c, func() {
			c.close()
		})
	}
	return err
}

func writeMsgpack(ws *websocket.Conn, msg messageParams) error {
	data, err := packet.Marshal(msg)
	if err == nil {
		packet := make([]byte, len(data)+1)
		packet[0] = byte(msg.msgType())
		copy(packet[1:], data)
		err = ws.WriteMessage(websocket.BinaryMessage, packet)
	}
	return err
}

func (c *client) connectionRefused(err error, peerid string, protocol string) {
	c.writeMsgpack(&smsgPeerConnectionRefusedParams{peerid, protocol, err.Error()})
}

func (c *client) newConnection(protocol string, peerid string, create func(conID uint64) *connection) {
	id := c.newConnectionID()
	c.read(create(id))
	c.writeMsgpack(&smsgPeerConnectionParams{strconv.FormatUint(id, 10), peerid, protocol})
}

func (c *client) checkProtocolErr(err error, typ messageType) {
	if err != nil {
		c.error(fmt.Sprintf("Bad message format for %v", typ.clientName()))
		c.close()
	}
}

func (r *relay) StartClient(c *client, init func(public bool, hasNat bool)) {
	r.handler.StartClient(c, init)
}

func (r *relay) Versions() (string, string) {
	return r.handler.Versions()
}

func (r *relay) Started() bool {
	return r.handler.Started()
}

func (r *relay) Start(treeProtocol string, treeName string, port uint16, pk string, friends []string) error {
	if err := r.handler.Start(treeProtocol, treeName, port, pk, friends); err != nil {return err}
	go func() {
		for {
			status := <-r.handler.PeerAccess()
			switch status {
			case network.ReachabilityUnknown:
				fmt.Printf("@@@@@ RECEIVED ACCESS CHANGED TO UNKNOWN\n")
			case network.ReachabilityPublic:
				fmt.Printf("@@@@@ RECEIVED ACCESS CHANGE TO PUBLIC\n")
			case network.ReachabilityPrivate:
				fmt.Printf("@@@@@ RECEIVED ACCESS CHANGE TO PRIVATE\n")
			}
			for _, c := range r.clients {
				svc(c, func() {
					if c.access != status {
						c.access = status
						sending := 0
						switch status {
						case network.ReachabilityUnknown:
							sending = 0
						case network.ReachabilityPrivate:
							sending = 1
						case network.ReachabilityPublic:
							sending = 2
						}
						c.writeMsgpack(&smsgAccessChangeParams{sending})
					}
				})
			}
		}
	}()
	return nil
}

func (r *relay) PeerAccess() chan network.Reachability {
	return r.handler.PeerAccess()
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

func (r *relay) Stop(c *client, protocol string, retainConnections bool) {
	r.handler.Stop(c, protocol, retainConnections)
}

func (r *relay) Close(c *client, conID uint64) {
	r.handler.Close(c, conID)
}

func (r *relay) Data(c *client, conID uint64, data []byte) {
	r.handler.Data(c, conID, data)
}

func (r *relay) Connect(c *client, protocol string, peerID string, frames bool, relay bool) {
	r.handler.Connect(c, protocol, peerID, frames, relay)
}

func (r *relay) Friends(add []string, remove []string) error {
	return r.handler.Friends(add, remove)
}

func (r *relay) CloseClient(c *client) {
	r.handler.CloseClient(c)
}

func (r *relay) init(handler protocolHandler) {
	r.clients = make(map[*websocket.Conn]*client)
	r.managementChan = make(chan func())
	r.handler = handler
}

func (r *relay) getSvcChannel() chan func() {
	return r.managementChan
}

func (r *relay) handleConnection() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		fmt.Println("GOT CONNECTION, STARTING WEB SOCKET")
		upgrader := websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		}
		con, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			log.Printf("error: %v", err)
		} else {
			if singleConnection && started {
				fmt.Println("CHECKING FOR OLD CONNECTIONS")
				alreadyConnected := svcSync(r, func() interface{} {
					// only allowing one client for now
					for range r.clients {
						return fmt.Errorf("Connected")
					}
					return nil
				})
				if alreadyConnected != nil {
					writeMsgpack(con, &smsgErrorParams{"There is already a connection"})
					con.Close()
					return
				}
			}
			started := r.Started()
			//fmt.Println("SENDING HELLO")
			v, _ := r.Versions()
			err := writeMsgpack(con, &smsgHelloParams{started, v})
			if err != nil {
				log.Printf("Error writing initial message: %v\n", err)
				con.Close()
				return
			}
			if !started {
				for {
					_, data, err := con.ReadMessage()
					if _, ok := err.(net.Error); ok && err.(net.Error).Timeout() {
						fmt.Println("CONTINUING WEB SOCKET READ AFTER TIMEOUT")
						err = nil
						continue
					}
					if err != nil && errors.Is(err, syscall.ETIMEDOUT) {
						fmt.Println("CONTINUING WEB SOCKET READ AFTER ETIMEDOUT")
						err = nil
						continue
					}
					if err == nil {
						fmt.Printf("@@@ READ MESSAGE %s: %X\n", messageType(data[0]).clientName(), data[1:])
					} else {
						fmt.Println("ERROR READING WEB SOCKET", err)
						con.Close()
					}
					if messageType(data[0]) == cmsgStart {
						msg := new(cmsgStartParams)
						_, err = packet.Unmarshal(data[1:], msg)
						if err != nil {
							fmt.Println("BAD START MESSAGE")
							con.Close()
							return
						}
						err = r.Start(msg.treeProtocol, msg.treeName, uint16(msg.port), msg.peerKey, msg.friends)
						if err != nil {
							fmt.Println("ERROR, BAD PORT:", msg.port)
							con.Close()
						} else {
							r.runProtocol(con)
						}
					} else {
						fmt.Println("ERROR, EXPECTED START MESSAGE BUT GOT", messageType(data[0]).clientName())
						con.Close()
					}
					// only continue loop with continue statement
					return
				}
			} else {
				r.runProtocol(con)
			}
		}
	}
}

func (r *relay) runProtocol(con *websocket.Conn) {
	svc(r, func() {
		client := r.CreateClient()
		client.control = con
		r.access = network.ReachabilityUnknown
		r.clients[con] = client
		// start websocket ping/pong keepalive
		con.SetReadDeadline(time.Now().Add(pongWait))
		con.SetPongHandler(func(string) error { con.SetReadDeadline(time.Now().Add(pongWait)); return nil })
		go func() {
			done := new(atomicBoolean)
			client.ticker = time.NewTicker(pingPeriod)
			defer client.ticker.Stop()
			for !done.Get() {
				_, ok := <-client.ticker.C
				if ok {
					svc(client, func() {
						if err := con.WriteMessage(websocket.PingMessage, nil); err != nil {
							done.Set(true)
							client.close()
						}
					})
				} else {
					break
				}
			}
		}()
		// start the client, send ident message when ready
		r.StartClient(client, func(public bool, hasNat bool) {
			_, v2 := r.Versions()
			client.writeMsgpack(&smsgIdentParams{public, hasNat, r.peerID, r.handler.AddressArray(), r.handler.PeerKey(), v2})
			runSvc(client)
			client.readWebsocket(r)
		})
	})
}

func newBuf(len int) *bytes.Buffer {
	return bytes.NewBuffer(make([]byte, len))
}

func array(item ...interface{}) []interface{} {
	return item[:]
}

func boolString(b bool) string {
	if b {return "true"}
	return "false"
}
