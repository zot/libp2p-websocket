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
 * This program demonstrate a simple chat application using p2p communication.
 *
 */

/*
# CLIENT-TO-SERVER MESSAGES
 
```
  Listen:      [0][FRAMES: 1][PROTOCOL: rest] -- request a listener for a protocol (frames optional)
  Stop:        [1][PROTOCOL: rest]            -- stop listening on PORT
  Close:       [2][ID: 8]                     -- close a stream
  Data:        [3][ID: 8][data: rest]         -- write data to stream
  Connect:     [4][FRAMES: 1][PROTOCOL: STR][PEERID: rest] -- connect to another peer (frames optional)
  Dsc Listen:  [5][FRAMES: 1][PROTOCOL: rest] -- host a protocol using discovery
  Dsc Connect: [6][FRAMES: 1][PROTOCOL: STR][PEERID: rest] -- request a connection to a peer potentially requesting a callback
```

# SERVER-TO-CLIENT MESSAGES

```
  Identify:                [0][PUBLIC: 1][PEERID: str]         -- successful initialization
  Listener Connection:     [1][ID: 8][PEERID: str][PROTOCOL: rest] -- new listener connection with id ID
  Connection Closed:       [2][ID: 8][REASON: rest]            -- connection ID closed
  Data:                    [3][ID: 8][data: rest]              -- receive data from stream with id ID
  Listen Refused:          [4][PROTOCOL: rest]                 -- could not listen on PROTOCOL
  Listener Closed:         [5][PROTOCOL: rest]                 -- could not listen on PROTOCOL
  Peer Connection:         [6][ID: 8][PEERID: str][PROTOCOL: rest] -- connected to a peer with id ID
  Peer Connection Refused: [7][PEERID: str][PROTOCOL: str][ERROR: rest] -- connection to peer PEERID refused
  Protocol Error:          [8][MSG: rest]                      -- error in the protocol
  Dsc Host Connect:        [9][ID: 8][PEERID: str][PROTOCOL: rest] -- connection from a discovery peer
  Dsc Peer Connect:        [10][ID: 8][PEERID: str][PROTOCOL: rest] -- connected to a discovery host
  Listening:               [11][PROTOCOL: rest]                -- confirmation that listening has started
```
*/
"use strict"
const protPat = /^\/x\//
const peerIdPat = /^[^/]+$/
const bytes = new ArrayBuffer(8);
const numberConverter = new DataView(bytes);

const natStatus = Object.freeze({
    unknown: 'unknown',
    public: 'public',
    private: 'private',
});

const cmsg = Object.freeze({
    listen: 0,
    stop: 1,
    close: 2,
    data: 3,
    connect: 4,
    discoveryListen: 5,
    discoveryConnect: 6,
});

const smsg = Object.freeze({
    ident: 0,
    listenerConnection: 1,
    connectionClosed: 2,
    data: 3,
    listenRefused: 4,
    listenerClosed: 5,
    peerConnection: 6,
    peerConnectionRefused: 7,
    error: 8,
    discoveryHostConnect: 9,
    discoveryPeerConnect: 10,
    listening: 11,
    discoveryAwaitingCallback: 12,
});

var ws;
var peerId;
var utfDecoder = new TextDecoder("utf-8");
var utfEncoder = new TextEncoder("utf-8");

function enumFor(enumObj, value) {
    for (var k in enumObj) {
        if (enumObj[k] == value) return k;
    }
    return null;
}

function sendObject(conid, object, callback) {
    console.log("SENDING COMMAND: "+JSON.stringify(object), object);
    sendString(conid, JSON.stringify(object), callback);
}

function sendString(conID, str, cb) {
    console.log("SENDING STRING TO CONNECTION ", conID, ": ", str)
    sendData(conID, utfEncoder.encode(str), cb);
}

function sendData(conID, data, cb) {
    var buf = new Uint8Array(9 + data.length);
    var dv = new DataView(buf.buffer);

    buf[0] = cmsg.data;
    dv.setBigUint64(1, conID);
    buf.set(data, 9)
    ws.send(buf, cb);
}

function close(conID, cb) {
    var buf = new Uint8Array(9);
    var dv = new DataView(buf.buffer);

    buf[0] = cmsg.close;
    dv.setBigUint64(1, conID);
    ws.send(buf, cb);
}

function listen(protocol, frames) {
    ws.send(Uint8Array.from([cmsg.listen, frames ? 1 : 0, ...utfEncoder.encode(protocol)]));
}

function discoveryListen(protocol, frames) {
    ws.send(Uint8Array.from([cmsg.discoveryListen, frames ? 1 : 0, ...utfEncoder.encode(protocol)]));
}

function stop(protocol) {
    ws.send(Uint8Array.from([cmsg.stop, ...utfEncoder.encode(protocol)]));
}

function connect(peerId, prot, frames, relay) {
    relay = relay || false;
    ws.send(Uint8Array.from([cmsg.connect,
                             frames ? 1 : 0,
                             relay ? 1 : 0,
                             prot.length >> 8, prot.length & 0xFF, ...utfEncoder.encode(prot),
                             ...utfEncoder.encode(peerId)]));
}

function discoveryConnect(peerId, prot, frames) {
    ws.send(Uint8Array.from([cmsg.discoveryConnect,
                             frames ? 1 : 0,
                             prot.length >> 8, prot.length & 0xFF, ...utfEncoder.encode(prot),
                             ...utfEncoder.encode(peerId)]));
}

// methods mimic the parameter order of the protocol
class BlankHandler {
    ident(publicPeer, peerId) {}
    listenerConnection(id, peerid, prot) {}
    connectionClosed(id, msg) {}
    data(id, data) {}
    listenRefused(protocol) {}
    listenerClosed(protocol) {}
    peerConnection(conid, peerid, prot) {}
    peerConnectionRefused(peerid, prot, msg) {}
    error(msg) {}
    discoveryHostConnect(conid, peerid, prot) {}
    discoveryPeerConnect(conid, peerid, prot) {}
    listening(protocol) {}
    discoveryAwaitingCallback(protocol) {}
}

class DelegatingHandler {
    constructor(delegate) {
        this.delegate = delegate;
    }
    ident(publicPeer, peerId) {
        this.delegate.ident(publicPeer, peerId);
    }
    listenerConnection(id, peerid, prot) {
        this.delegate.listenerConnection(id, peerid, prot);
    }
    connectionClosed(id, msg) {
        this.delegate.connectionClosed(id, msg);
    }
    data(id, data) {
        this.delegate.data(id, data);
    }
    listenRefused(protocol) {
        this.delegate.listenRefused(protocol);
    }
    listenerClosed(protocol) {
        this.delegate.listenerClosed(protocol);
    }
    peerConnection(conid, peerid, prot) {
        this.delegate.peerConnection(conid, peerid, prot);
    }
    peerConnectionRefused(peerid, prot, msg) {
        this.delegate.peerConnectionRefused(peerid, prot, msg);
    }
    error(msg) {
        this.delegate.error(msg);
    }
    discoveryHostConnect(conid, peerid, prot) {
        this.delegate.discoveryHostConnect(conid, peerid, prot);
    }
    discoveryPeerConnect(conid, peerid, prot) {
        this.delegate.discoveryPeerConnect(conid, peerid, prot);
    }
    listening(protocol) {
        this.delegate.listening(protocol);
    }
    discoveryAwaitingCallback(protocol) {
        this.delegate.discoveryAwaitingCallback(protocol);
    }
}

function receivedMessageArgs(type, args) {
    console.log("RECEIVED MESSAGE: ", type, ...args);
}

class LoggingHandler extends DelegatingHandler {
    constructor(delegate) {
        super(delegate);
    }
    ident(publicPeer, peerId) {
        receivedMessageArgs('ident', arguments);
        this.delegate.ident(publicPeer, peerId);
    }
    listenerConnection(id, peerid, prot) {
        receivedMessageArgs('listenerConnection', arguments);
        this.delegate.listenerConnection(id, peerid, prot);
    }
    connectionClosed(id, msg) {
        receivedMessageArgs('connectionClosed', arguments);
        this.delegate.connectionClosed(id, msg);
    }
    data(id, data) {
        receivedMessageArgs('data', arguments);
        this.delegate.data(id, data);
    }
    listenRefused(protocol) {
        receivedMessageArgs('listenRefused', arguments);
        this.delegate.listenRefused(protocol);
    }
    listenerClosed(protocol) {
        receivedMessageArgs('listenerClosed', arguments);
        this.delegate.listenerClosed(protocol);
    }
    peerConnection(conid, peerid, prot) {
        receivedMessageArgs('peerConnection', arguments);
        this.delegate.peerConnection(conid, peerid, prot);
    }
    peerConnectionRefused(peerid, prot, msg) {
        receivedMessageArgs('peerConnectionRefused', arguments);
        this.delegate.peerConnectionRefused(peerid, prot, msg);
    }
    error(msg) {
        receivedMessageArgs('error', arguments);
        this.delegate.error(msg);
    }
    discoveryHostConnect(conid, peerid, prot) {
        receivedMessageArgs('discoveryHostConnect', arguments);
        this.delegate.discoveryHostConnect(conid, peerid, prot);
    }
    discoveryPeerConnect(conid, peerid, prot) {
        receivedMessageArgs('discoveryPeerConnect', arguments);
        this.delegate.discoveryPeerConnect(conid, peerid, prot);
    }
    listening(protocol) {
        receivedMessageArgs('listening', arguments);
        this.delegate.listening(protocol)
    }
    discoveryAwaitingCallback(protocol) {
        receivedMessageArgs('discoveryAwaitingCallback', arguments);
        this.delegate.discoveryAwaitingCallback(protocol);
    }
}

class TrackingHandler extends DelegatingHandler {
    constructor(delegate) {
        super(delegate);
        delegate.incomingConnections = new Map();
        delegate.outgoingConnections = new Map();
        delegate.natStatus = natStatus.unknown;
        delegate.listeningTo = new Set();
        delegate.awaitingCallbacks = new Set();
    }
    ident(publicPeer, peerId) {
        this.delegate.peerId = peerId;
        this.delegate.natStatus = publicPeer ? natStatus.public : natStatus.private;
        super.ident(publicPeer, peerId);
    }
    listenerConnection(id, peerid, prot) {
        this.delegate.incomingConnections.set(id, prot);
        super.listenerConnection(id, peerid, prot);
    }
    connectionClosed(id, msg) {
        this.delegate.incomingConnections.delete(id);
        this.delegate.outgoingConnections.delete(id);
        super.connectionClosed(id, msg);
    }
    data(id, data) {
        super.data(id, data);
    }
    listenRefused(protocol) {
        super.listenRefused(protocol);
    }
    listenerClosed(protocol) {
        this.delegate.listeningTo.delete(protocol);
        super.listenerClosed(protocol);
    }
    peerConnection(conid, peerid, prot) {
        this.delegate.outgoingConnections.set(conid, prot);
        super.peerConnection(conid, peerid, prot);
    }
    peerConnectionRefused(peerid, prot, msg) {
        super.peerConnectionRefused(peerid, prot, msg);
    }
    error(msg) {
        super.error(msg);
    }
    discoveryHostConnect(conid, peerid, prot) {
        this.delegate.incomingConnections.set(conid, prot);
        super.discoveryHostConnect(conid, peerid, prot);
    }
    discoveryPeerConnect(conid, peerid, prot) {
        this.delegate.outgoingConnections.set(conid, prot);
        this.delegate.awaitingCallbacks.delete(prot);
        super.discoveryPeerConnect(conid, peerid, prot);
    }
    listening(protocol) {
        this.delegate.listeningTo.add(protocol);
        super.listening(protocol);
    }
    discoveryAwaitingCallback(protocol) {
        this.delegate.awaitingCallbacks.add(protocol);
        super.discoveryAwaitingCallback(protocol);
    }
}

function start(urlStr, handler) {
    ws = new WebSocket(urlStr);
    ws.onopen = function open() {
        console.log("OPENED CONNECTION, WAITING FOR PEER ID AND NAT STATUS...");
    };
    ws.onerror = err=> console.log('Error: ', err);
    ws.onmessage = msg=> {
        msg.data.arrayBuffer().then(buf => {
            var data = new Uint8Array(buf);
            //var dv = new DataView(new Uint8Array(data));
            var dv = new DataView(buf);

            console.log("MESSAGE: [", data.join(", "), "]");
            console.log("TYPE: ", enumFor(smsg, data[0]));
            switch (data[0]) {
            case smsg.ident: {
                var publicPeer = data[1]
                peerId = getString(data.slice(2));
                handler.ident(publicPeer, getString(data.slice(2)));
                break;
            }
            case smsg.listenerConnection: {
                var id = dv.getBigUint64(1);
                var peerid = getCountedString(dv, 9);
                var prot = getString(data.slice(9 + 2 + peerid.length));

                handler.listenerConnection(id, peerid, prot);
                break;
            }
            case smsg.connectionClosed: {
                var id = dv.getBigUint64(1);
                var reason = getString(data.slice(9));

                handler.connectionClosed(id, reason);
                break;
            }
            case smsg.data: {
                var id = dv.getBigUint64(1);
                handler.data(id, data.slice(9));
                break;
            }
            case smsg.listenRefused:
                handler.listenRefused(getString(data.slice(1)));
                break;
            case smsg.listenerClosed:
                handler.listenerClosed(getString(data.slice(1)));
                break;
            case smsg.peerConnection:
                var conid = dv.getBigUint64(1);
                var peerid = getCountedString(dv, 9);
                var prot = getString(data.slice(9 + 2 + peerid.length));

                handler.peerConnection(conid, peerid, prot)
                break;
            case smsg.peerConnectionRefused: {
                var prot = getCountedString(dv, 1);
                var peeridStart = prot.length + 2 + 1;
                var peerid = getCountedString(dv, peeridStart);
                var msg = getString(data.slice(peeridStart + peerid.length + 2));

                handler.peerConnectionRefused(peerid, prot, msg);
                break;
            }
            case smsg.error:
                handler.error(getString(data.slice(1)));
                break;
            case smsg.discoveryHostConnect:
                var conid = dv.getBigUint64(1)
                var peerid = getCountedString(dv, 9)
                var prot = getString(data.slice(9 + peerid.length + 2))

                handler.discoveryHostConnect(conid, peerid, prot);
                break;
            case smsg.discoveryPeerConnect:
                var conid = dv.getBigUint64(1)
                var peerid = getCountedString(dv, 9)
                var prot = getString(data.slice(9 + peerid.length + 2))

                handler.discoveryPeerConnect(conid, peerid, prot);
                break;
            case smsg.listening:
                handler.listening(getString(data.slice(1)));
                break;
            case smsg.discoveryAwaitingCallback:
                handler.discoveryAwaitingCallback(getString(data.slice(1)));
                break;
            }
        });
    }
}

function getCountedString(dv, offset) {
    var start = dv.byteOffset + offset;

    return getString(dv.buffer.slice(start + 2, start + 2 + dv.getUint16(offset)));
}

function getString(buf) {
    return utfDecoder.decode(buf);
}

function checkProt(str) {
    if (!str.match(protPat)) {
        return 'Bad protocol format, protocols must begin with /x/ but this protocol is '+str;
    }
}

function checkPeerId(str) {
    if (!str.match(peerIdPat)) {
        return 'Bad peer ID format, peer ids must not contain slashes this id is '+str;
    }
}
    
export default {
    start,
    BlankHandler,
    TrackingHandler,
    LoggingHandler,
    checkProt,
    checkPeerId,
    sendString,
    sendObject,
    sendData,
    stop,
    listen,
    connect,
    discoveryListen,
    discoveryConnect,
    getString,
    close,
}
