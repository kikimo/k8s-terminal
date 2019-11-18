package main

import (
	"encoding/json"
	"github.com/gorilla/websocket"
	"k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

var (
	staticDir string
	clientset *kubernetes.Clientset
	kconfig   *rest.Config
)

// message from web socket client
type xtermMessage struct {
	MsgType string `json:"type"`  // 类型:resize客户端调整终端, input客户端输入
	Input   string `json:"input"` // msgtype=input情况下使用
	Rows    uint16 `json:"rows"`  // msgtype=resize情况下使用
	Cols    uint16 `json:"cols"`  // msgtype=resize情况下使用
}

type WSStreamHandler struct {
	conn        *websocket.Conn
	resizeEvent chan remotecommand.TerminalSize
	rbuf        []byte
	cond        *sync.Cond

	sync.Mutex
}

// Run start a loop to fetch from ws client and store the data in byte buffer
func (h *WSStreamHandler) Run() {
	for {
		_, p, err := h.conn.ReadMessage()
		if err != nil {
			// TODO
		}
		xmsg := xtermMessage{}
		if err := json.Unmarshal(p, &xmsg); err != nil {
			// TODO
		}

		switch xmsg.MsgType {
		case "input":
			{
				h.Lock()
				// log.Printf("reading input: %s", string(xmsg.Input))
				h.rbuf = append(h.rbuf, xmsg.Input...)
				h.cond.Signal()
				h.Unlock()
			}
		case "resize":
			{
				ev := remotecommand.TerminalSize{
					Width:  xmsg.Cols,
					Height: xmsg.Rows}
				h.resizeEvent <- ev
			}
		default:
			// ignore
		}
	}
}

func (h *WSStreamHandler) Read(b []byte) (size int, err error) {
	h.Lock()
	for len(h.rbuf) == 0 {
		h.cond.Wait()
	}
	size = copy(b, h.rbuf)
	h.rbuf = h.rbuf[size:]

	h.Unlock()

	return
}

func (h *WSStreamHandler) Write(b []byte) (size int, err error) {
	size = len(b)

	os.Stdout.Write(b)
	// log.Printf("writing msg with size: %d to web socket", len(b))
	err = h.conn.WriteMessage(websocket.TextMessage, b)

	return
}

func (h *WSStreamHandler) Next() (size *remotecommand.TerminalSize) {
	ret := <-h.resizeEvent
	size = &ret

	return
}

func wsHandler(resp http.ResponseWriter, req *http.Request) {
	var (
		conn          *websocket.Conn
		sshReq        *rest.Request
		podName       string
		podNs         string
		containerName string
		executor      remotecommand.Executor
		handler       *WSStreamHandler
		err           error
	)

	// 解析GET参数
	if err = req.ParseForm(); err != nil {
		return
	}
	podNs = req.Form.Get("podNs")
	podName = req.Form.Get("podName")
	containerName = req.Form.Get("containerName")

	// 得到websocket长连接
	upgrader := websocket.Upgrader{}
	if conn, err = upgrader.Upgrade(resp, req, nil); err != nil {
		log.Fatalf("error creating ws conn: %v", err)
	}

	sshReq = clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(podNs).
		SubResource("exec").
		VersionedParams(&v1.PodExecOptions{
			Container: containerName,
			Command:   []string{"bash"},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	// 创建到容器的连接
	if executor, err = remotecommand.NewSPDYExecutor(kconfig, "POST", sshReq.URL()); err != nil {
		log.Fatalf("error creating spdy executor: %v", err)
	}

	log.Println("connectingi to pod...")
	handler = &WSStreamHandler{
		conn:        conn,
		resizeEvent: make(chan remotecommand.TerminalSize)}
	handler.cond = sync.NewCond(handler)

	// run loop to fetch data from ws client
	go handler.Run()

	err = executor.Stream(remotecommand.StreamOptions{
		Stdin:             handler,
		Stdout:            handler,
		Stderr:            handler,
		TerminalSizeQueue: handler,
		Tty:               true,
	})

	return
}

func main() {
	staticDir = "./public"
	fs := http.FileServer(http.Dir(staticDir))
	http.Handle("/", fs)

	var err error
	cfgpath := filepath.Join(homeDir(), ".kube/config")
	if kconfig, err = clientcmd.BuildConfigFromFlags("", cfgpath); err != nil {
		log.Fatalf("error creating k8s config: %v", err)
	}

	if clientset, err = kubernetes.NewForConfig(kconfig); err != nil {
		log.Fatalf("error creating clientset: %v", err)
	}

	log.Printf("%v", clientset)
	http.HandleFunc("/terminal", wsHandler)

	log.Println("running...")
	http.ListenAndServe(":8000", nil)
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}

	return os.Getenv("USERPROFILE") // windows
}
