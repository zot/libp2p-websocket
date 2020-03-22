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

//TODO clear a client's callback monitors when it disconnects

package main

import (
	"os"
	"context"
	"flag"
	"fmt"
	"net/http"
	"path/filepath"
	"log"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
	"strings"
	"encoding/ascii85"
	"encoding/json"
	"bytes"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/libp2p/go-libp2p-discovery"
	//basicHost "github.com/libp2p/go-libp2p/p2p/host/basic"
	//circuit "github.com/libp2p/go-libp2p-circuit"
	//rly "github.com/libp2p/go-libp2p/p2p/host/relay"
	//routing "github.com/libp2p/go-libp2p-routing"
	autonat "github.com/libp2p/go-libp2p-autonat"
	//pubsub "github.com/libp2p/go-libp2p-pubsub"
	//pb "github.com/libp2p/go-libp2p-pubsub/pb"

	//dht "github.com/libp2p/go-libp2p-kad-dht"
	multiaddr "github.com/multiformats/go-multiaddr"
	logging "github.com/whyrusleeping/go-logging"

	goLog "github.com/ipfs/go-log"
	//"github.com/mr-tron/base58/base58"
	autonatSvc "github.com/libp2p/go-libp2p-autonat-svc"

	"github.com/pkg/browser"
)

/*
 * Parts of this were taken from Abhishek Upperwal and Mantas Vidutis' libp2p chat example,
 * https://github.com/libp2p/go-libp2p-examples/tree/master/chat-with-rendezvous
 * and are Copyright (c) 2018 Protocol Labs, also licensed with the MIT license
 */

const (
	rendezvousString = "p2pmud2"
	discoveryDirectPrefix = "libp2p-connection-direct"
	discoveryIndirectPrefix = "libp2p-connection-indirect"
	discoveryCallbackPrefix = "libp2p-connection-callback"
	dscTTL = 5 * time.Minute
	//callbackMonitorTTL = 1 * time.Minute
	callbackMonitorTTL = 15 * time.Second
	callbackFrequency = 1 * time.Minute
)

type addrList []multiaddr.Multiaddr

type fileList []string

type retryError string

type libp2pRelay struct {
	relay
	host host.Host
	discovery *discovery.RoutingDiscovery
	natStatus autonat.NATStatus
	natActions []func()                          // defer these until nat status known
	connectedPeers map[peer.ID]*libp2pConnection // connected peers
}

type libp2pClient struct {
	client
	listeners map[string]*listener           // protocol -> listener
	listenerConnections map[uint64]*listener // connectionID -> listener
	forwarders map[uint64]*libp2pConnection  // connectionID -> forwarder
	connecting map[string]bool               // attempting discovery connect with these protocols
	monitoring map[string]bool               // callbacks this peer is monitoring
}

type libp2pConnection struct {
	connection
	peerID peer.ID
}

type listener struct {
	client *libp2pClient                     // the client that owns this listener
	connections map[uint64]*libp2pConnection // connectionID -> connection
	protocol string
	frames bool                              // whether to transmit frame lengths
	managementChan chan func()               // client management
//	closing func(*listerner)                 // callback
	closed bool
}

var centralRelay *libp2pRelay
var starting bool
var logger = goLog.Logger("p2pmud")
var peerKey string
var listenAddresses addrList
var fakeNatStatus string
var bootstrapPeers addrList
var bootstrapPeerStrings = []string{
	"/dnsaddr/bootstrap.libp2p.io/ipfs/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
	"/dnsaddr/bootstrap.libp2p.io/ipfs/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
	"/dnsaddr/bootstrap.libp2p.io/ipfs/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
	"/dnsaddr/bootstrap.libp2p.io/ipfs/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
	"/ip4/104.131.131.82/tcp/4001/ipfs/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ",
	"/ip4/104.236.179.241/tcp/4001/ipfs/QmSoLPppuBtQSGwKDZT2M73ULpjvfd3aZ6ha4oFGL1KrGM",
	"/ip4/104.236.76.40/tcp/4001/ipfs/QmSoLV4Bbm51jM9C4gDYZQ9Cy3U6aXMJDAbzgu2fzaDs64",
	"/ip4/128.199.219.111/tcp/4001/ipfs/QmSoLSafTMBsPKadTEgaXctDQVcqN88CNLHXMkTNwMKPnu",
	"/ip4/178.62.158.247/tcp/4001/ipfs/QmSoLer265NRgSp2LA3dPaeykiS1J6DifTC88f5uVQKNAd",
	"/ip6/2400:6180:0:d0::151:6001/tcp/4001/ipfs/QmSoLSafTMBsPKadTEgaXctDQVcqN88CNLHXMkTNwMKPnu",
	"/ip6/2604:a880:1:20::203:d001/tcp/4001/ipfs/QmSoLPppuBtQSGwKDZT2M73ULpjvfd3aZ6ha4oFGL1KrGM",
	"/ip6/2604:a880:800:10::4a:5001/tcp/4001/ipfs/QmSoLV4Bbm51jM9C4gDYZQ9Cy3U6aXMJDAbzgu2fzaDs64",
	"/ip6/2a03:b0c0:0:1010::23:1001/tcp/4001/ipfs/QmSoLer265NRgSp2LA3dPaeykiS1J6DifTC88f5uVQKNAd",
}
var peerFinder interface {
	FindPeer(context.Context, peer.ID) (peer.AddrInfo, error)
}

var logCount int = 1

func (err retryError) Error() string {
	if err == "" {
		return "Retry error"
	}
	return string(err)
}

func createLibp2pRelay() *libp2pRelay {
	r := new(libp2pRelay)
	_, ok := interface{}(r).(discoveryHandler)
	if !ok {
		log.Fatal("libp2pRelay does not support discoveryHandler interface!")
	}
	r.init(r)
	r.connectedPeers = make(map[peer.ID]*libp2pConnection)
	return r
}

func getLibp2pRelay(r *relay) *libp2pRelay {
	return r.handler.(*libp2pRelay)
}

func (r *libp2pRelay) libp2pClient(c *client) *libp2pClient {
	return getLibp2pClient(c)
}

func (r *libp2pRelay) whenNatKnown(f func()) {
	svc(r, func() {
		if r.natStatus != autonat.NATStatusUnknown {
			f()
		} else {
			r.natActions = append(r.natActions, f)
		}
	})
}

func (r *libp2pRelay) CreateClient() *client {
	c := new(libp2pClient)
	c.client.init(&r.relay, c)
	c.listeners = make(map[string]*listener)
	c.listenerConnections = make(map[uint64]*listener)
	c.forwarders = make(map[uint64]*libp2pConnection)
	c.connecting = make(map[string]bool)
	c.monitoring = make(map[string]bool)
	return &c.client
}

func (r *libp2pRelay) CleanupClosed(con *connection) {
	delete(getLibp2pClient(con.client).connecting, con.protocol)
}

// LISTEN API METHOD
func (r *libp2pRelay) Listen(c *client, prot string, frames bool) {
	r.listen(smsgListening, smsgNewConnection, r.libp2pClient(c), prot, frames, nil)
}

func (r *libp2pRelay) listen(announceMsg messageType, msgType messageType, c *libp2pClient, prot string, frames bool, connect func(*libp2pConnection)) *listener {
	lis := c.createListener(prot, frames)
	fmt.Println("listen, protocol: ", prot, ", frames: ", frames)
	r.host.SetStreamHandler(protocol.ID(prot), func(stream network.Stream) {
		fmt.Println("GOT A CONNECTION")
		svc(c, func() {
			con := c.createConnection(c.newConnectionID(), prot, stream, frames)
			fmt.Printf("GOT DIRECT CONNECTION ON %s FROM %s\n", prot, stream.Conn().RemotePeer().Pretty())
			lis.connections[con.id] = con
			c.listenerConnections[con.id] = lis
			c.writePackedMessage(msgType, con.id, stream.Conn().RemotePeer().Pretty(), prot)
			c.read(&con.connection)
			if connect != nil {
				connect(con)
			}
		})
	})
	c.writePackedMessage(announceMsg, prot)
	return lis
}

// STOP LISTENER API METHOD
func (r *libp2pRelay) Stop(c *client, protocol string, retainConnections bool) {
	lc := getLibp2pClient(c)
	listener := lc.listeners[protocol]
	if listener != nil {
		listener.close(retainConnections)
	} else if lc.monitoring[protocol] {
		delete(lc.monitoring, protocol)
		c.writePackedMessage(smsgListenerClosed, protocol)
	}
}

func (r *libp2pRelay) StartClient(c *client, init func(public bool)) {
	go func() {
		r.whenNatKnown(func() {
			var public bool

			switch r.natStatus {
			case autonat.NATStatusUnknown:
				fmt.Println("!!! UNKNOWN")
				public = true
			case autonat.NATStatusPublic:
				fmt.Println("!!! PUBLIC")
				public = true
			case autonat.NATStatusPrivate:
				fmt.Println("!!! PRIVATE")
				public = false
			}
			init(public)
		})
	}()
}

func (r *libp2pRelay) HasConnection(c *client, id uint64) bool {
	return getLibp2pClient(c).hasConnection(id)
}

// CLOSE STREAM API METHOD
func (r *libp2pRelay) Close(c *client, id uint64) {
	lis := getLibp2pClient(c).listenerConnections[id]
	if lis != nil {
		fmt.Printf("CLOSING HOST CONNECTION %d\n", id)
		lis.removeConnection(id, false)
	}
	fwd := getLibp2pClient(c).forwarders[id]
	if fwd != nil {
		fmt.Printf("CLOSING PEER CONNECTION %d\n", id)
		fwd.close(func() {
			delete(getLibp2pClient(c).forwarders, id)
		})
	}
}

// SEND DATA API METHOD
func (r *libp2pRelay) Data(c *client, id uint64, data []byte) {
	lc := getLibp2pClient(c)
	con := lc.forwarders[id]

	if con == nil {
		lis := lc.listenerConnections[id]
		if lis != nil {
			con = lis.connections[id]
		}
	}
	if con != nil {
		fmt.Println("@@@ WRITING DATA TO CONNECTION")
		con.writeData(&r.relay, data)
	} else {
		fmt.Println("@@@ WRITING DATA TO CONNECTION")
		c.writePackedMessage(smsgConnectionClosed, id, "unknown connection")
	}
}

// CONNECT API METHOD
func (r *libp2pRelay) Connect(c *client, prot string, peerid string, frames bool, relay bool) {
	type addrs struct {
		PeerID string
		Addrs []string // the addrs of the peer
	}
	//var encodedAddrs addrs
	encodedAddrs := new(addrs)
	var addrInfo peer.AddrInfo
	relayMsg := "out"

	if relay {
		relayMsg = ""
	}
	if strings.HasPrefix(peerid, "/addrs/") {
		enc := strings.TrimPrefix(peerid, "/addrs/")
		dst := make([]byte, len(enc))
		ndst, _, err := ascii85.Decode(dst, []byte(enc), true)
		fmt.Println("Decoded", ndst, "bytes, len(dst) =", len(dst))
		if err != nil {
			c.connectionRefused(fmt.Errorf("Could not decode addrs: %s\n", peerid), peerid, prot)
			return
		}
		fmt.Println("Decoding", string(dst[:ndst]))
		//err = json.Unmarshal(dst[:ndst], &encodedAddrs)
		err = json.Unmarshal(dst[:ndst], encodedAddrs)
		if err != nil {
			c.connectionRefused(fmt.Errorf("Could not decode addrs: %s\n", string(dst[:ndst])), peerid, prot)
			return
		}
		peerid = encodedAddrs.PeerID
		fmt.Println("Peer ID:", peerid)
		fmt.Printf("Addrs: %#v\n", encodedAddrs)
		addrInfo.Addrs = make([]multiaddr.Multiaddr, len(encodedAddrs.Addrs))
		for i, addr := range encodedAddrs.Addrs {
			ma, err := multiaddr.NewMultiaddr(addr)
			if err != nil {
				c.connectionRefused(fmt.Errorf("Could not decode peer addr: %s\n", addr), peerid, prot)
				return
			}
			addrInfo.Addrs[i] = ma
		}
	}
	pid, err := peer.Decode(peerid)
	if err != nil {
		c.connectionRefused(fmt.Errorf("Error parsing peer id %s: %s", peerid, err), peerid, prot)
		return
	}
	addrInfo.ID = pid
/*
	fmt.Printf("Finding address for peer %s\n", pid.Pretty())
	addr, err := peerFinder.FindPeer(context.Background(), pid)
	if err != nil {
		c.connectionRefused(fmt.Errorf("Error finding peer %s: %s\n", pid.Pretty(), err.Error()), peerid, prot)
		return
	} else if len(addr.Addrs) == 0 {
		c.connectionRefused(fmt.Errorf("Could not find peer %s\n", pid.Pretty()), peerid, prot)
		return
	}
	fmt.Printf("Attempting to connect peer %s\n", pid.Pretty())
	err = r.host.Connect(context.Background(), addr)
	if err != nil {
		c.connectionRefused(fmt.Errorf("Could not connect to peer %s: %s\n", pid.Pretty(), err.Error()), pid.Pretty(), prot)
		return
	}
*/
	if encodedAddrs.PeerID == "" {
		fmt.Printf("Attempting to connect peer %s\n", pid.Pretty())
		maddr, err := multiaddr.NewMultiaddr("/p2p/"+peerid)
		if err != nil {
			c.connectionRefused(fmt.Errorf("Could not parse multiaddr %s\n", "/p2p/"+peerid), pid.Pretty(), prot)
			return
		}
		addrInfo.ID = pid
		addrInfo.Addrs = []multiaddr.Multiaddr{maddr}
	}
	err = r.host.Connect(context.Background(), addrInfo)
	if err != nil {
		c.connectionRefused(fmt.Errorf("Could not connect to peer %s: %s\n", pid.Pretty(), err.Error()), pid.Pretty(), prot)
		return
	}
	fmt.Printf("Attempting to connect with protocol %v to peer %v with%s relay\n", prot, peerid, relayMsg)
	if !relay {

		stream, err := r.host.NewStream(context.Background(), pid, protocol.ID(prot))
		if err != nil {
			fmt.Println("COULDN'T OPEN STREAM,", err)
			c.connectionRefused(err, peerid, prot)
			return
		}
		fmt.Println("Connected")
		lc := r.libp2pClient(c)
		c.newConnection(smsgPeerConnection, prot, stream.Conn().RemotePeer().Pretty(), func(conID uint64) *connection {
			con := lc.createConnection(conID, prot, stream, frames)
			lc.forwarders[conID] = con
			return &con.connection
		})
/*
	} else {
		r.retryLoop(c, prot, peerid, pid, frames, relay, 0, func(con *libp2pConnection){}, func(err error) {
			c.connectionRefused(err, peerid, prot)
		})
*/
	}
}

/*
func (r *libp2pRelay) retryLoop(c *client, prot string, peerid string, pid peer.ID, frames bool, relay bool, count int, successFunc func(con *libp2pConnection), errFunc func(err error)) {
	if count > 4 {
		errFunc(retryError("Failed after 5 attempts to find address for "+peerid))
	} else {
		fmt.Printf("Connect attempt #%d\n", count)
		con, err := r.tryConnect(context.Background(), r.libp2pClient(c), pid, prot, frames, relay, count)
		if err == nil {
			successFunc(con)
			return
		}
		_, ok := err.(retryError)
		if ok {
			go func() {
				time.Sleep(1000 * time.Millisecond)
				r.retryLoop(c, prot, peerid, pid, frames, relay, count + 1, successFunc, errFunc)
			}()
			return
		} else {
			errFunc(err)
		}
	}
}
*/

func direct(protocol string, frames bool) string {
	prot := discoveryDirectPrefix
	if !frames {
		prot += "raw-"
	}
	return prot + protocol
}

func indirect(protocol string, frames bool) string {
	prot := discoveryIndirectPrefix
	if !frames {
		prot += "raw-"
	}
	return prot + protocol
}

func callback(pid peer.ID, protocol string, frames bool) string {
	transport := "framed"
	if !frames {
		transport = "raw"
	}
	return fmt.Sprintf("%s/%s/%s/%s", protocol, discoveryCallbackPrefix, transport, pid.Pretty())
}

func (r *libp2pRelay) DiscoveryListen(c *client, frames bool, prot string) {
	lc := r.libp2pClient(c)
	go func() {
		switch r.natStatus {
		case autonat.NATStatusUnknown:
			fmt.Println("NAT STATUS UNKNOWN; QUEUING DISCOVERY LISTEN UNTIL NAT STATUS IS KNOWN")
			r.whenNatKnown(func() {
				status := "PUBLIC"
				if r.natStatus == autonat.NATStatusPrivate {
					status = "PRIVATE"
				}
				fmt.Printf("NAT STATUS NOW KNOWN TO BE %s; EXECUTING DISCOVERY LISTEN\n", status)
				r.DiscoveryListen(c, frames, prot)
			})
			return
		case autonat.NATStatusPublic:
			r.listen(smsgListening, smsgDscHostConnect, lc, prot, frames, nil)
		case autonat.NATStatusPrivate:
			c.writePackedMessage(smsgListening, prot)
			r.monitorCallbackRequests(lc, frames, prot)
			// use normal listen for relaying
			//l := r.listen(smsgListening, smsgDscHostConnect, lc, prot, frames, nil)
			//go func() {
			//	closed := false
			//	for !closed {
			//		//addr, err := an.PublicAddr()
			//		//if err == nil {
			//		//	fmt.Println("@@@ PUBLIC ADDRESS: ", addr)
			//			r.printAddresses()
			//			time.Sleep(20 * time.Second)
			//			svcSync(lc, func() interface{} {
			//				closed = l.closed
			//				return nil
			//			})
			//		//}
			//	}
			//}()
		}
	}()
}

func logLine(str string, items ...interface{}) {
	log.Output(2, fmt.Sprintf("[%d] %s", logCount, fmt.Sprintf(str, items...)))
	logCount++
}

// monitor the callback channel of the prot protocol for requests and call each one back
func (r *libp2pRelay) monitorCallbackRequests(c *libp2pClient, frames bool, prot string) {
	cbprot := callback(r.host.ID(), prot, frames)
	fmt.Println("START MONITORING FOR CALLBACK REQUESTS: "+cbprot)
	svc(c, func() {
		c.monitoring[prot] = true
		lis := c.createListener(cbprot, frames)
		killChan := make(chan bool, 1)
		go func() {
			syncChan := make(chan bool)
			called := make(map[peer.ID]time.Time) // only call back a peer once per minute
			for { logLine("monitorCallbackRequests 1")
				fmt.Println("Starting a callback peers pass")
				svc(c, func() {
					syncChan <- lis.closed || !c.running || !c.monitoring[prot]
				})
				if <-syncChan {
					logLine("stop monitoring "+cbprot)
					killChan <- true
					break
				}
				fmt.Println("SEARCHING FOR PEERS ON "+cbprot)
				// find peers for a limited time
				ctx, _ := context.WithDeadline(context.Background(), time.Now().Add(callbackMonitorTTL))
				//ctx := context.Background()
				peerChan, err := r.discovery.FindPeers(ctx, cbprot)
				if err != nil {
					logLine("Error finding peers: %s", err.Error())
					lis.close(false)
					break
				}
				count := 0
				for peerChan != nil { logLine("monitorCallbackRequests 2")
					select {
					case peer, ok := <-peerChan:
						if !ok {
							peerChan = nil
						} else if peer.ID != "" {
							fmt.Println("FOUND CALLBACK REQUEST ON", cbprot, "FROM", peer.ID.Pretty())
							count++
							go c.callbackPeer(cbprot, frames, peer, called)
						}
					case <-ctx.Done():
						fmt.Println("Find callback peers pass on", cbprot, "finished after", count, "peers")
						break
					case <-killChan:
						break
					}
				}
			}
			logLine("done with monitorCallbackRequests for "+cbprot)
			svc(c, func() {
				delete(c.monitoring, prot)
			})
		}()
	})
}

func (r *libp2pRelay) DiscoveryConnect(c *client, frames bool, prot string, peerid string) {
/*
	// TODO CREATE CANCELLATION MESSAGE SO USER CAN CANCEL A DISCOVERY, THEN REMOVE COUNT
	logLine("DiscoveryConnect")
	pid, err := peer.Decode(peerid)
	if err != nil {
		c.connectionRefused(err, peerid, prot)
		return
	}
	lc := r.libp2pClient(c)
	if !lc.connecting[prot] { // attempt both direct and callback request
		lc.connecting[prot] = true
		connected := new(atomicBoolean)
		cbprot := callback(pid, prot, frames)
		var cancel context.CancelFunc

		r.retryLoop(c, prot, peerid, pid, frames, false, 0, func(con *libp2pConnection) {connected.Set(true)}, func(err error) {
			log.Printf("Could not make direct connection to %s on protocol %s.\nError: %s\nError: %#v\nwaiting for callback...", peerid, prot, err.Error(), err)
		})
		//go func() {
		//	err = r.tryConnect(context.Background(), r.libp2pClient(c), pid, prot, frames)
		//	if err == nil {
		//		connected.Set(true)
		//	} else {
		//		log.Printf("Could not make direct connection to %s on protocol %s.\nError: %s\nError: %#v\nwaiting for callback...", peerid, prot, err.Error(), err)
		//	}
		//}()
		go func() {
			var lis *listener
			count := 0

			lis = r.listen(smsgDscAwaitingCallback, smsgDscPeerConnect, lc, cbprot, frames, func(con *libp2pConnection) { // got our callback
				fmt.Println("Connected to listener via indirect callback")
				connected.Set(true)
				lc.connecting[prot] = false
				lis.closePrim()
				lc.forwarders[con.id] = con
				cancel()
			})
			for ; count < 5 && !connected.Get(); count++ {
				var ctx context.Context

				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(dscTTL))
				fmt.Println("Advertising for callback: "+cbprot)
				discovery.Advertise(ctx, r.discovery, cbprot, discovery.TTL(dscTTL + 5 * time.Second))
				<-ctx.Done() // wait for advertisement to expire
			}
			if !connected.Get() {
				fmt.Printf("Could not find peer for %s after %d attempts\n", prot, count)
				svc(c, func() {lc.connecting[prot] = false})
			}
		}()
	}
*/
}

/*
func (r *libp2pRelay) tryConnect(ctx context.Context, c *libp2pClient, peerID peer.ID, prot string, frames bool, relay bool, count int) (*libp2pConnection, error) {
	if relay {
		//can, err := circuit.CanHop(ctx, r.host, peerID)
		//if err != nil {
		//	fmt.Println("CANNOT HOP,", err)
		//	return nil, retryError(err.Error())
		//}
		//if !can {
		//	//return nil, fmt.Errorf("Cannot reach %s with circuit-relay", peerID.Pretty())
		//	fmt.Println("CANNOT HOP,", err)
		//	return nil, retryError(err.Error())
		//}
		fmt.Println("Attempting circuit-relay connection")
		//relayaddr, err := multiaddr.NewMultiaddr(bootstrapPeerStrings[0]+"/p2p-circuit/p2p/" + peerID.Pretty())
		if count >= len(rly.DefaultRelays) {return nil, fmt.Errorf("No more public relays")}
		relayaddr, err := multiaddr.NewMultiaddr(rly.DefaultRelays[count]+"/p2p-circuit/p2p/" + peerID.Pretty())
		//relayaddr, err := multiaddr.NewMultiaddr("/p2p-circuit/p2p/" + peerID.Pretty())
		checkErr(err)
		fmt.Println("Using multiaddr:", relayaddr)
		err = r.host.Connect(ctx, peer.AddrInfo{
			ID: peerID,
			Addrs: []multiaddr.Multiaddr{relayaddr},
		})
		if err != nil {
			fmt.Println("Failed to connect over relay:\n", err)
			return nil, retryError(err.Error())
		}
	} else {
		addr, err := peerFinder.FindPeer(context.Background(), peerID)
		if err != nil {
			fmt.Printf("Error finding peer: %s\n", err.Error())
			return nil, err
		} else if len(addr.Addrs) == 0 {
			return nil, retryError("No addresses for peer "+peerID.Pretty())
		}
	}
	stream, err := r.host.NewStream(ctx, peerID, protocol.ID(prot))
	if err != nil {
		fmt.Println("COULDN'T OPEN STREAM,", err)
		return nil, err
	}
	fmt.Println("Connected")
	var con *libp2pConnection
	c.newConnection(smsgPeerConnection, prot, stream.Conn().RemotePeer().Pretty(), func(conID uint64) *connection {
		con = c.createConnection(conID, prot, stream, frames)
		c.forwarders[conID] = con
		return &con.connection
	})
	return con, nil
}
*/

func (r *libp2pRelay) printAddresses() {
	fmt.Println("Addresses:")
	//for _, addr := range r.host.(*basicHost.BasicHost).AllAddrs() {
	for _, addr := range r.host.Addrs() {
		fmt.Println("   ", addr.String()+"/p2p/"+r.peerID)
	}
}

func (r *libp2pRelay) AddressesJson() string {
	buf := bytes.NewBuffer(make([]byte, 0, 16))
	fmt.Println("Getting addresses...")
	buf.WriteByte(byte('['))
	first := true
	for _, addr := range r.host.Addrs() {
		if first {
			first = false
		} else {
			buf.WriteByte(byte(','))
		}
		buf.WriteByte(byte('"'))
		buf.Write([]byte(addr.String()))
		buf.WriteByte(byte('"'))
		fmt.Println("Address: " + addr.String())
	}
	buf.WriteByte(byte(']'))
	return string(buf.Bytes())
}

func (r *libp2pRelay) setNATStatus(status autonat.NATStatus) {
	r.natStatus = status
	for _, f := range r.natActions {
		f()
	}
	r.natActions = []func(){}
}

func createListener() *listener {
	lis := new(listener)
	lis.connections = make(map[uint64]*libp2pConnection)
	lis.managementChan = make(chan func())
	return lis
}

func (l *listener) close(retainConnections bool) {
	for id := range l.connections {
		l.removeConnection(id, retainConnections)
	}
	l.client.writePackedMessage(smsgListenerClosed, l.protocol)
	l.closePrim()
}

func (l *listener) closePrim() {
	l.client.libp2pRelay().host.RemoveStreamHandler(protocol.ID(l.protocol))
	delete(l.client.listeners, l.protocol)
	for conId, _ := range l.connections {
		delete(l.client.listenerConnections, conId)
	}
	l.closed = true
}

func (l *listener) removeConnection(id uint64, retainConnections bool) {
	if retainConnections {
		fmt.Println("RETAINING SERVICE CONNECTION ", id)
		svc(l.client, func() {
			l.client.forwarders[id] = l.connections[id]
			delete(l.client.listenerConnections, id)
		})
	} else {
		fmt.Println("CLOSING SERVICE CONNECTION ", id)
		l.connections[id].close(func() {
			svc(l.client, func() {
				delete(l.client.listenerConnections, id)
			})
		})
	}
	delete(l.connections, id)
}

func getLibp2pClient(c *client) *libp2pClient {
	return c.data.(*libp2pClient)
}

func (c *libp2pClient) libp2pRelay() *libp2pRelay {
	return getLibp2pRelay(c.relay)
}

func (c *libp2pClient) hasConnection(conID uint64) bool {
	return c.listenerConnections[conID] != nil || c.forwarders[conID] != nil
}

func (c *libp2pClient) createConnection(conID uint64, prot string, stream network.Stream, frames bool) *libp2pConnection {
	con := new(libp2pConnection)
	con.peerID = stream.Conn().RemotePeer()
	con.connection.init("connection", prot, conID, stream, &c.client, frames, con)
	fmt.Println("MAKING CONNECTION WITH ID ", con.id)
	svc(c.relay, func(){c.libp2pRelay().connectedPeers[con.peerID] = con})
	return con
}

func (c *libp2pClient) createListener(prot string, frames bool) *listener {
	lis := createListener()
	lis.frames = frames
	lis.protocol = prot
	c.listeners[prot] = lis
	lis.client = c
	return lis
}

func (c *libp2pClient) callbackPeer(prot string, frames bool, peer peer.AddrInfo, peers map[peer.ID]time.Time) {
	earliest := time.Now().Add(-callbackFrequency)
	t, found := peers[peer.ID]
	if !found || t.Before(earliest) { logLine("callbackPeer")
		peers[peer.ID] = time.Now()
		stream, err := c.libp2pRelay().host.NewStream(context.Background(), peer.ID, protocol.ID(prot))
		if err != nil {
			fmt.Printf("COULD NOT CALL BACK PEER %s on %s: %s\n", peer.ID.Pretty(), prot, err.Error())
			return
		}
		fmt.Printf("GOT INDIRECT CONNECTION ON %s FROM %s\n", prot, stream.Conn().RemotePeer().Pretty())
		svc(c, func() {
			c.newConnection(smsgDscHostConnect, prot, stream.Conn().RemotePeer().Pretty(), func(conID uint64) *connection {
				con := c.createConnection(conID, prot, stream, frames)
				c.forwarders[conID] = con
				return &con.connection
			})
		})
	}
}

func getLibp2pConnection(con *connection) *libp2pConnection {
	return con.data.(*libp2pConnection)
}

func checkErrWithMsg(err error, msg string) {
	if err != nil {
		fmt.Println(msg)
		panic(err)
	}
}

func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}

func initp2p() {
	starting = true
	goLog.SetAllLoggers(logging.WARNING)
	goLog.SetLogLevel("rendezvous", "info")
	ctx := context.Background()
	//var routingDht atomic.Value
	opts := []libp2p.Option{
		libp2p.NATPortMap(),
		//libp2p.DefaultTransports,
		//libp2p.DefaultMuxers,
		//libp2p.DefaultSecurity,
		//libp2p.EnableRelay(),
		//libp2p.EnableAutoRelay(),
		//libp2p.AddressFactory(func(addrs []ma.Multiaddr) []multiaddr.Multiaddr {
		//	return append(addrs, multiaddr.StringCast(bsaddr.Encapsulate(multiaddr.StringCast("/p2p-circuit"))))
		//}),
		//libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
		//	d, err := dht.New(ctx, h)
		//	routingDht.Store(d)
		//	return d, err
		//}),
		//libp2p.EnableRelay(circuit.OptHop),
		//libp2p.EnableRelay(circuit.OptHop, circuit.OptActive),
		//libp2p.DefaultStaticRelays(),
		//libp2p.EnableRelay(),
		libp2p.ListenAddrs([]multiaddr.Multiaddr(listenAddresses)...),
	}
	if peerKey != "" { // add peer key into opts if provided
		keyBytes, err := crypto.ConfigDecodeKey(peerKey)
		checkErr(err)
		key, err := crypto.UnmarshalPrivateKey(keyBytes)
		opts = append(opts, libp2p.Identity(key))
	}
	// libp2p.New constructs a new libp2p Host. Other options can be added
	// here.
	myHost, err := libp2p.New(ctx, opts...)
	checkErr(err)
	logger.Info("Host created. We are:", myHost.ID())
	logger.Info(myHost.Addrs())
	centralRelay.peerID = myHost.ID().Pretty()
	centralRelay.host = myHost

	if fakeNatStatus == "public" {
		centralRelay.setNATStatus(autonat.NATStatusPublic)
	} else if fakeNatStatus == "private" {
		centralRelay.setNATStatus(autonat.NATStatusPrivate)
	} else {
		/// MONITOR NAT STATUS
		fmt.Println("Creating autonat")
		//an := autonat.NewAutoNAT(ctx, myHost, nil)
		an := autonat.NewAutoNAT(context.Background(), myHost, nil)
		centralRelay.natStatus = autonat.NATStatusUnknown
		go func() {
			peeped := false
			for {
				status := an.Status()
				svcSync(centralRelay, func() interface{} {
					if status != centralRelay.natStatus || !peeped {
						switch status {
						case autonat.NATStatusUnknown:
							fmt.Println("@@@ NAT status UNKNOWN")
							addr, err := an.PublicAddr()
							if err == nil {
								fmt.Println("@@@ PUBLIC ADDRESS: ", addr)
								centralRelay.printAddresses()
							}
						case autonat.NATStatusPublic:
							fmt.Println("@@@ NAT status PUBLIC")
							addr, err := an.PublicAddr()
							if err == nil {
								fmt.Println("@@@ PUBLIC ADDRESS: ", addr)
								centralRelay.printAddresses()
							}
						case autonat.NATStatusPrivate:
							fmt.Println("@@@ NAT status PRIVATE")
						}
						if status != autonat.NATStatusUnknown {
							centralRelay.setNATStatus(status)
						}
					}
					return nil
				})
				peeped = true
				time.Sleep(250 * time.Millisecond)
			}
		}()
	}
	key := myHost.Peerstore().PrivKey(myHost.ID())
	keyBytes, err := crypto.MarshalPrivateKey(key)
	checkErr(err)
	keyString := crypto.ConfigEncodeKey(keyBytes)
	fmt.Printf("host private %s key: %s\n", reflect.TypeOf(key), keyString)

	autonatSvc.NewAutoNATService(context.Background(), myHost)

//	// Start a DHT, for use in peer discovery. We can't just make a new DHT
//	// client because we want each peer to maintain its own local copy of the
//	// DHT, so that the bootstrapping node of the DHT can go down without
//	// inhibiting future peer discovery.
//	kademliaDHT, err := dht.New(ctx, myHost)
////	kademliaDHT := routingDht.Load().(*dht.IpfsDHT)
//	checkErr(err)
//
//	peerFinder = kademliaDHT
	// Bootstrap the DHT. In the default configuration, this spawns a Background
	// thread that will refresh the peer table every five minutes.
//	logger.Debug("Bootstrapping the DHT")
//	checkErr(kademliaDHT.Bootstrap(ctx))

	// Let's connect to the bootstrap nodes first. They will tell us about the
	// other nodes in the network.
	var wg sync.WaitGroup
	var remaining int32 = int32(len(bootstrapPeers))
	fmt.Printf("@@@ WAITING FOR %d bootstrap peer connections...\n", remaining)
	for _, peerAddr := range bootstrapPeers {
		peerinfo, err := peer.AddrInfoFromP2pAddr(peerAddr)
		if err != nil {continue}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := myHost.Connect(ctx, *peerinfo); err != nil {
				logger.Warning(err)
			} else {
				logger.Info("Connection established with bootstrap node:", *peerinfo)
			}
			rem := atomic.AddInt32(&remaining, -1)
			fmt.Printf("@@@ WAITING FOR %d bootstrap peer connections...\n", rem)
		}()
	}
	wg.Wait()

	// We use a rendezvous point "meet me here" to announce our location.
	// This is like telling your friends to meet you at the Eiffel Tower.
	logger.Info("Announcing ourselves...")
	//centralRelay.discovery = discovery.NewRoutingDiscovery(kademliaDHT)
//	centralRelay.discovery = discovery.NewRoutingDiscovery(kademliaDHT)
	//err := myDHT.PutValue(context.Background(), "p2pmud", []byte(centralRelay.host.ID().Pretty()))
	//checkErr(err)
	//mudChan := myDHT.Search(context.Background(), "p2pmud")
	//discovery.Advertise(ctx, centralRelay.discovery, rendezvousString, discovery.TTL(1 * time.Minute))
	//logger.Debug("Successfully announced!")

	// Now, look for others who have announced
	// This is like your friend telling you the location to meet you.
	//logger.Debug("Searching for other peers...")
	//peerChan, err := centralRelay.discovery.FindPeers(ctx, config.RendezvousString)
	//_, err = centralRelay.discovery.FindPeers(ctx, rendezvousString)
	//checkErr(err)
/*
	peerChan, err := centralRelay.discovery.FindPeers(ctx, rendezvousString) // request just to get in touch with peers
	if err != nil {
		panic(err)
	}
	go func() {
		fmt.Println("SEARCHING FOR PEERS...")
		for peer := range peerChan {
			if peer.ID == centralRelay.host.ID() {
				continue
			}
			logger.Debug("Found peer:", peer)
		}
		fmt.Println("FINISHED SEARCHING FOR PEERS")
	}()
*/
	centralRelay.printAddresses()
	fmt.Println("FINISHED INITIALIZING P2P, CREATING RELAY")
	runSvc(centralRelay)
	fmt.Printf("Peer id: %v\n", centralRelay.peerID)
}

func (fl *fileList) String() string {
	strs := make([]string, len(*fl))
	for i, file := range *fl {
		strs[i] = string(file)
	}
	return strings.Join(strs, ",")
}

func (fl *fileList) Set(value string) error {
	_, err := os.Stat(value)
	if err != nil {
		return err
	}
	*fl = append(*fl, value)
	return nil
}

func (al *addrList) String() string {
	strs := make([]string, len(*al))
	for i, addr := range *al {
		strs[i] = addr.String()
	}
	return strings.Join(strs, ",")
}

func (al *addrList) Set(value string) error {
	addr, err := multiaddr.NewMultiaddr(value)
	if err != nil {
		return err
	}
	*al = append(*al, addr)
	return nil
}

func StringsToAddrs(addrStrings []string) (maddrs []multiaddr.Multiaddr, err error) {
	for _, addrString := range addrStrings {
		addr, err := multiaddr.NewMultiaddr(addrString)
		if err != nil {
			return maddrs, err
		}
		maddrs = append(maddrs, addr)
	}
	return
}

func main() {
	starting = false
	disconnected := false
	log.SetFlags(log.Lshortfile)
	browse := ""
	centralRelay = createLibp2pRelay()
	addr := "localhost"
	port := 8888
	noBootstrap := false
	bootstrapArg := addrList([]multiaddr.Multiaddr{})
	fileList := fileList([]string{})
	fakeNATPrivate := false
	fakeNATPublic := false
	flag.BoolVar(&noBootstrap, "nopeers", false, "clear the bootstrap peer list")
	flag.StringVar(&peerKey, "key", "", "specify peer key")
	flag.Var(&fileList, "files", "add the contents of a directory to serve from /")
	flag.StringVar(&addr, "addr", "", "host address to listen on")
	flag.IntVar(&port, "port", port, "port to listen on")
	flag.Var(&bootstrapArg, "peer", "Adds a peer multiaddress to the bootstrap list")
	flag.Var(&listenAddresses, "listen", "Adds a multiaddress to the listen list")
	flag.StringVar(&browse, "browse", "", "Browse a URL")
	flag.BoolVar(&fakeNATPrivate, "fakenatprivate", false, "pretend nat is private")
	flag.BoolVar(&fakeNATPublic, "fakenatpublic", false, "pretend nat is publc")
	flag.BoolVar(&disconnected, "disconnected", false, "allow web client to start peer")
	flag.Parse()
	if len(bootstrapArg) > 0 {
		bootstrapPeers = bootstrapArg
	} else {
		bootstrapPeers, _ = StringsToAddrs(bootstrapPeerStrings)
	}
	if fakeNATPrivate {
		fakeNatStatus = "private"
	} else if fakeNATPublic {
		fakeNatStatus = "public"
	}
	if !disconnected {
		initp2p()
	}
	fmt.Printf("Listening on port %v\n", port)
	http.HandleFunc("/ipfswsrelay/start", func(w http.ResponseWriter, req *http.Request) {
		if disconnected && !starting {
			fmt.Println("STARTING RELAY...")
			/// GRAB KEY FROM REQUEST BODY
			initp2p()
			fmt.Println("STARTED")
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	http.HandleFunc("/ipfswsrelay", centralRelay.handleConnection())
	if len(fileList) > 0 {
		for _, dir := range fileList {
			fmt.Println("File dir: ", dir)
		}
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			for _, dir := range fileList {
				reqFile, err := filepath.Abs(filepath.Join(dir, r.URL.Path))
				if err != nil {continue}
				_, err = os.Stat(reqFile)
				if err != nil {continue}
				http.ServeFile(w, r, reqFile)
				return
			}
			http.Error(w, "Not found", http.StatusNotFound)
		})
	} else {
		http.Handle("/", http.FileServer(FS(false)))
	}
	if browse != "" {
		browser.OpenURL(fmt.Sprintf("http://localhost:%d/%s", port, browse))
	}
	log.Fatal(http.ListenAndServe(fmt.Sprintf("%s:%d", addr, port), nil))
}
