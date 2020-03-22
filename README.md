# LIBP2P-WEBSOCKET

Relay p2p connections between browsers transcoding between sockets and websockets

This runs a simple websocket server and listens for websocket connections using a simple protocol:

## NOTE: THIS IS A WORK IN PROGRESS

# LICENSE AND COPYRIGHT
Copyright (c) 2020, William R. Burdick Jr., Roy Riggs, and TEAM CTHLUHU. All rights reserved.
Use of this source code is governed by an MIT-style
license that can be found in the LICENSE file.

# Running

`./libp2p-websocket -browser chat.html` will start the relay and pop the chat example in a browser

In development, you can use -files to point to the live html/js/css files you are editing. If you are in the src directory. For example, you should be able to use this command to pop out a development version of chat:
```
libp2p-websocket -files html -files examples -browse chat.html
```

## Usage:
```
Usage of libp2p-websocket:
  -addr string
        host address to listen on
  -browse string
        Browse a URL
  -files value
        add the contents of a directory to serve from /
  -key string
        specify peer key
  -listen value
        Adds a multiaddress to the listen list
  -nopeers
        clear the bootstrap peer list
  -peer value
        Adds a peer multiaddress to the bootstrap list
  -port int
        port to listen on (default 8888)
```

# libp2p-websocket runs a websocket server on /libp2p
This allows a browser to control the relay using a very simple binary protocol.
When a connection closes, it cleans up all of its child connections.
The client and server exchange these command messages, with the first byte of each message identifying the command.

# CLIENT-TO-SERVER MESSAGES
 
```
  Start:       [0][KEY: str] -- start peer with optional peer key
  Listen:      [1][FRAMES: 1][PROTOCOL: rest] -- request a listener for a protocol (frames optional)
  Stop:        [2][PROTOCOL: rest] -- stop listening to PROTOCOL
  Close:       [3][ID: 8]                     -- close a stream
  Data:        [4][ID: 8][data: rest]         -- write data to stream
  Connect:     [5][FRAMES: 1][PROTOCOL: STR][RELAY: STR][PEERID: rest] -- connect to another peer (frames optional)
```

## Including peer addresses with the peerid

The connect message allows a peer ID or a peer ID plus its addresses. This allows connection to peers without relying on discovery techniques. Users can exchange addresses over other channels, like chat programs.

A peer ID plus its addresses are encoded as

`/addrs/BASE85JSON` where BASE85JSON is a JSON object encoded in base85. The JSON object is like this:

```json
{
    "peerID": PEERID,
    "addrs": [MULTIADDR,...]
}

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
```

# Building

## Prerequisites
**go**: you need [go](https://golang.org/) if you want to build the go part (which includes updating the default HTML files)

**esc:** `go get -u github.com/mjibson/esc` -- make sure this is on your path (it should go into $GOHOME/bin).

## Building the default webdir

The `src/build` file is (probably?) a posix shell script that uses esc to generate files.go by combining examples/* and html/* into a directory and creating a virtual file system out of which the HTTP server serves files.

## build-and-run

Go compiles so fast that, while I'm developing, I build the entire project everytime I run it:

```shell
cd src
./build && go build libp2p-websocket.go protocol.go files.go && ./libp2p-websocket -browse chat.html
```
This creates an updated files.go, compiles the project, and then runs the chat example.
