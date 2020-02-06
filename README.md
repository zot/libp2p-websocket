# IPFS-P2P-WEBSOCKET

Relay p2p connections between browsers transcoding between sockets and websockets

This runs a simple websocket server and listens for websocket connections using a simple protocol:

## NOTE: THIS IS A WORK IN PROGRESS

# LICENSE AND COPYRIGHT
Copyright 2020 Bill Burdick. All rights reserved.
Use of this source code is governed by a BSD-style
license that can be found in the LICENSE file.

# /ws/control
Allow browser to control the relay over a websocket using a very simple binary protocol.
When a connection closes, clean up all of its child connections.
The client and server exchange these command messages, with the first byte of each message identifying the command.

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
