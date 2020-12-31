package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	torrentLogger "github.com/anacrolix/log"
	"github.com/anacrolix/torrent"
	torrentStorage "github.com/anacrolix/torrent/storage"
	"github.com/gorilla/websocket"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ip         = ""
	port       = "8080"
	filesDir   = "files"
	torrentDir = "torrent_tmp"
	https      = true
)

var maxIDRetries = 5000
var ids []string
var existingIds map[string]bool

const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func generateID(n int) string {
	s := make([]byte, n)
	for i := range s {
		s[i] = letters[rand.Intn(len(letters))]
	}
	return string(s)
}

func getID() (string, error) {
	id := ""
	for i := 0; i < maxIDRetries; i++ {
		if existingIds[id] || id == "" {
			id = generateID(4)
		} else {
			break
		}
	}

	if id == "" {
		return "", errors.New("too many retries")
	}

	existingIds[id] = true
	idsFile, err := os.OpenFile("ids", os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		log.Fatalf("Error appending to ids file: %v", err)
	}

	fmt.Fprintf(idsFile, "\n%s", id)
	idsFile.Close()

	return id, nil
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		http.ServeFile(w, r, "index.html")
	} else {
		path := "files/" + r.URL.Path[1:]
		http.ServeFile(w, r, path)
	}
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	file, header, err := r.FormFile("file")
	if err != nil {
		log.Printf("Error getting form file: %v", err)
		return
	}
	defer file.Close()

	filename, err := saveFile(file, header.Filename)
	if err != nil {
		log.Printf("Error saving file: %v", err)
		return
	}

	prefix := "http"
	if https {
		prefix += "s"
	}

	fmt.Fprintf(w, prefix+"://"+r.Host+"/"+filename)
}

func saveFile(file io.Reader, filename string) (string, error) {
	id, err := getID()
	if err != nil {
		return "", errors.New("Can't get new id")
	}

	savename := fmt.Sprintf("%v", id)

	split := strings.Split(filename, ".")
	if len(split) > 1 {
		savename = fmt.Sprintf("%v.%v", id, split[len(split)-1])
	}

	dst, err := os.Create(filesDir + "/" + savename)
	if err != nil {
		return "", err
	}
	defer dst.Close()

	_, err = io.Copy(dst, file)
	if err != nil {
		return "", err
	}

	return savename, nil
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func downloadProgress(done chan bool, savePath string, total int64, ws *websocket.Conn, ID int, inputName string) {
	stop := false
	for {
		select {
		case <-done:
			stop = true
		default:
			file, err := os.Open(savePath)
			if err != nil {
				log.Printf("Error opening file: %v", err)
				return
			}

			stat, err := file.Stat()
			if err != nil {
				log.Printf("Error getting file stat: %v", err)
				return
			}
			size := stat.Size()

			if size == 0 {
				size = 1
			}

			var progress = int(float64(size) / float64(total) * 100)
			output := fmt.Sprintf(`{"id": %d, "progress": %d, "size": %d, "name": "%s"}`, ID, progress, total, inputName)
			err = ws.WriteMessage(1, []byte(output))
			if err != nil {
				log.Printf("downloadProgress: Can't write message: %v", err)
				return
			}
		}
		if stop {
			return
		}
		time.Sleep(time.Second)
	}
}

type Input struct {
	ID  int
	URL string
}

func downloadFromUrl(input Input, ws *websocket.Conn, r *http.Request) (string, error) {
	_, err := url.ParseRequestURI(input.URL)
	if err != nil {
		return fmt.Sprintf(`{"id": %d, "error": "Bad url"}`, input.ID), err
	}
	inputName := path.Base(input.URL)

	savename, err := getID()
	if err != nil {
		return fmt.Sprintf(`{"id": %d, "error": "Error downloading file"}`, input.ID), err
	}

	split := strings.Split(input.URL, ".")
	if len(split) > 1 {
		ext := split[len(split)-1]
		re := regexp.MustCompile("([A-Za-z0-9]+)")
		m := re.FindStringSubmatch(ext)
		savename = fmt.Sprintf("%v.%v", savename, m[0])
	}

	savePath := "files/" + savename
	dst, err := os.Create(savePath)
	defer dst.Close()

	res, err := http.Get(input.URL)
	if err != nil {
		return fmt.Sprintf(`{"id": %d, "error": "Bad url"}`, input.ID), err
	}
	defer res.Body.Close()

	done := make(chan bool)
	sizeInt, err := strconv.Atoi(res.Header.Get("Content-Length"))
	if err != nil {
		return fmt.Sprintf(`{"id": %d, "error": "Bad url"}`, input.ID), err
	}
	size := int64(sizeInt)

	go downloadProgress(done, savePath, size, ws, input.ID, inputName)

	_, err = io.Copy(dst, res.Body)
	if err != nil {
		return fmt.Sprintf(`{"id": %d, "error": "Error downloading file"}`, input.ID), err
	}

	done <- true

	prefix := "http"
	if https {
		prefix += "s"
	}

	return fmt.Sprintf(`{"id": %d, "url": "`+prefix+`://`+r.Host+`/`+savename+`", "size": %d, "name": "%s"}`,
		input.ID, size, inputName), nil
}

func tarDir(torrentPath string, savename string) error {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)
	defer tw.Close()

	first := true
	err := filepath.Walk(torrentPath, func(path string, info os.FileInfo, err error) error {
		if first {
			first = false
			return nil
		}

		if err != nil {
			return err
		}

		if !info.Mode().IsRegular() {
			return nil
		}

		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}

		header.Name = strings.TrimPrefix(strings.Replace(path, torrentPath, "", -1), string(filepath.Separator))
		header.Name = strings.Replace(header.Name, strings.Replace(torrentPath, "/", string(filepath.Separator), -1), "", -1)

		err = tw.WriteHeader(header)
		if err != nil {
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}

		_, err = io.Copy(tw, file)
		if err != nil {
			return err
		}

		file.Close()
		return nil
	})
	if err != nil {
		return err
	}

	err = tw.Close()
	if err != nil {
		return err
	}

	err = ioutil.WriteFile("files/"+savename+".tar", buf.Bytes(), 0644)
	if err != nil {
		return err
	}
	return nil
}

var torrentClient *torrent.Client

func torrentProgress(done chan bool, ws *websocket.Conn, ID int, total int64, t *torrent.Torrent) {
	stop := false
	for {
		select {
		case <-done:
			stop = true
		default:
			var progress = int(float64(t.BytesCompleted()) / float64(total) * 100)
			output := fmt.Sprintf(`{"id": %d, "progress": %d, "size": %d, "name": "%s"}`, ID, progress, total, t.Info().Name)
			err := ws.WriteMessage(1, []byte(output))
			if err != nil {
				log.Printf("torrentProgress: Can't write message: %v", err)
				return
			}
		}
		if stop {
			return
		}
		time.Sleep(time.Second)
	}
}

func downloadFromMagnet(input Input, ws *websocket.Conn, r *http.Request) (string, error) {
	spec, err := torrent.TorrentSpecFromMagnetUri(input.URL)
	if err != nil {
		return "", err
	}
	compl := torrentStorage.NewMapPieceCompletion()

	savename, err := getID()
	if err != nil {
		return "", err
	}

	torrentPath := fmt.Sprintf("%s/%s/", torrentDir, savename)

	mmap := torrentStorage.NewMMapWithCompletion(torrentPath, compl)
	spec.Storage = mmap
	t, _, err := torrentClient.AddTorrentSpec(spec)
	if err != nil {
		return "", err
	}

	for t.Info() == nil {
	}

	torrentSize := t.BytesMissing()
	done := make(chan bool)
	go torrentProgress(done, ws, input.ID, torrentSize, t)

	t.DisallowDataUpload()
	t.DownloadAll()

	for t.BytesMissing() > 0 {
	}

	err = mmap.Close()
	if err != nil {
		return "", err
	}

	t.Drop()
	done <- true

	err = tarDir(torrentPath, savename)
	if err != nil {
		return "", err
	}

	err = os.RemoveAll(torrentPath)
	if err != nil {
		return "", err
	}

	prefix := "http"
	if https {
		prefix += "s"
	}

	return fmt.Sprintf(`{"id": %d, "url": "`+prefix+`://`+r.Host+`/`+savename+`.tar", "size": %d, "name": "%s.tar"}`,
		input.ID, torrentSize, savename), nil
}

func fromURLHandler(w http.ResponseWriter, r *http.Request) {
	upgrader.CheckOrigin = func(r *http.Request) bool {
		return true
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Error upgrading connection to ws: %v", err)
		return
	}

	for {
		_, mess, err := ws.ReadMessage()
		if err != nil {
			log.Printf("Error reading message: %v", err)
			break
		}

		var input Input
		err = json.Unmarshal(mess, &input)
		if err != nil {
			log.Printf("Error parsing json: %v", err)
			continue
		}

		var output string
		if strings.HasPrefix(input.URL, "magnet:?") {
			output, err = downloadFromMagnet(input, ws, r)
		} else {
			output, err = downloadFromUrl(input, ws, r)
		}

		if err != nil {
			log.Println(err)
		}

		err = ws.WriteMessage(1, []byte(output))
		if err != nil {
			log.Printf("Can't write message: %v", err)
			break
		}
	}
}

func getIDsFromFile() {
	existingIds = make(map[string]bool)

	info, err := os.Stat("ids")

	if err == nil && info.IsDir() {
		log.Fatalf("ids dir exist")
	}
	if os.IsNotExist(err) {
		createFile, err := os.Create("ids")
		if err != nil {
			log.Fatalf("Error creating ids file: %v", err)
		}
		createFile.Close()
	}

	idsFile, err := os.Open("ids")
	if err != nil {
		log.Fatalf("Error opening ids file: %v", err)
	}
	defer idsFile.Close()

	scanner := bufio.NewScanner(idsFile)
	for scanner.Scan() {
		existingIds[scanner.Text()] = true
	}
}

func main() {
	var err error
	rand.Seed(time.Now().UnixNano())
	os.Mkdir(filesDir, 0700)
	getIDsFromFile()

	config := torrent.NewDefaultClientConfig()
	config.DataDir = torrentDir
	config.Logger = torrentLogger.Discard
	torrentClient, err = torrent.NewClient(config)
	if err != nil {
		log.Fatalf("Can't start torrent client: %v", err)
	}

	http.HandleFunc("/", indexHandler)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))
	http.HandleFunc("/upload", uploadHandler)
	http.HandleFunc("/fromurl", fromURLHandler)

	if https {
		log.Printf("Serving https on %v:%v\n", ip, port)
		err = http.ListenAndServeTLS(ip+":"+port, "../cert", "../key", nil)
		if err != nil {
			log.Fatalf("Error serving https at %v:%v : %v", ip, port, err)
		}
	} else {
		log.Printf("Serving http on %v:%v\n", ip, port)
		err = http.ListenAndServe(ip+":"+port, nil)
		if err != nil {
			log.Fatalf("Error serving http at %v:%v : %v", ip, port, err)
		}
	}
}
