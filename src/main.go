package main

import "fmt"
import "github.com/libp2p/go-libp2p"

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
