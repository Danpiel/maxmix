package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// VB-CABLE (free, signed virtual audio cable by VB-Audio). Its render endpoint is
// "CABLE Input", the paired capture endpoint is "CABLE Output".
const (
	cableDownloadURL = "https://download.vb-audio.com/Download_CABLE/VBCABLE_Driver_Pack45.zip"
	cableSetupExe    = "VBCABLE_Setup_x64.exe"
	cableEndpoint    = "CABLE Input"
)

func cableInstalled() bool {
	eps, err := listEndpoints(eRender)
	if err != nil {
		return false
	}
	_, ok := findEndpoint(eps, cableEndpoint)
	return ok
}

// locateSetup returns an existing VB-CABLE installer, downloading the driver
// pack into %LOCALAPPDATA%\maxmix\vbcable if none is present.
func locateSetup() (string, error) {
	for _, dir := range []string{
		`C:\Program Files\VB\CABLE`,
		`C:\Program Files (x86)\VB\CABLE`,
		filepath.Join(os.Getenv("LOCALAPPDATA"), "maxmix", "vbcable"),
	} {
		p := filepath.Join(dir, cableSetupExe)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "maxmix", "vbcable")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	zipPath := filepath.Join(dir, "VBCABLE_Driver_Pack.zip")
	log.Printf("downloading %s", cableDownloadURL)
	if err := download(cableDownloadURL, zipPath); err != nil {
		return "", err
	}
	if err := unzip(zipPath, dir); err != nil {
		return "", err
	}
	p := filepath.Join(dir, cableSetupExe)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%s not found in downloaded archive", cableSetupExe)
	}
	return p, nil
}

func download(url, dst string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func unzip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		name := filepath.Clean(f.Name)
		if strings.HasPrefix(name, "..") {
			continue
		}
		out := filepath.Join(dst, name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(out, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.Create(out)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(w, rc)
		w.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// shellExecuteInfo mirrors SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	Size       uint32
	Mask       uint32
	Hwnd       syscall.Handle
	Verb       *uint16
	File       *uint16
	Parameters *uint16
	Directory  *uint16
	Show       int32
	InstApp    syscall.Handle
	IDList     uintptr
	Class      *uint16
	KeyClass   syscall.Handle
	HotKey     uint32
	Icon       syscall.Handle
	Process    syscall.Handle
}

const (
	seeMaskNoCloseProcess = 0x40
	swShowNormal          = 1
)

var (
	shell32            = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteEx = shell32.NewProc("ShellExecuteExW")
)

// runElevated launches exe with args through the UAC prompt and waits for exit.
func runElevated(exe, args string) error {
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	params, _ := syscall.UTF16PtrFromString(args)
	dir, _ := syscall.UTF16PtrFromString(filepath.Dir(exe))
	info := shellExecuteInfo{
		Mask:       seeMaskNoCloseProcess,
		Verb:       verb,
		File:       file,
		Parameters: params,
		Directory:  dir,
		Show:       swShowNormal,
	}
	info.Size = uint32(unsafe.Sizeof(info))
	ok, _, callErr := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return fmt.Errorf("ShellExecuteEx(runas %s): %w", exe, callErr)
	}
	if info.Process == 0 {
		return errors.New("installer did not start")
	}
	defer syscall.CloseHandle(info.Process)
	_, err := syscall.WaitForSingleObject(info.Process, syscall.INFINITE)
	return err
}

// installCable installs VB-CABLE (silent mode) and waits for its endpoint.
func installCable() error {
	if cableInstalled() {
		fmt.Printf("%s already present.\n", cableEndpoint)
		return nil
	}
	setup, err := locateSetup()
	if err != nil {
		return err
	}
	// The installer makes the new cable the system default device; remember the
	// current defaults and put them back once the cable is up.
	before, derr := currentDefaults()
	fmt.Printf("Running %s -i -h (UAC prompt; accept the driver signature dialog if shown)\n", setup)
	if err := runElevated(setup, "-i -h"); err != nil {
		return err
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if cableInstalled() {
			fmt.Printf("%s is active.\n", cableEndpoint)
			time.Sleep(2 * time.Second) // let the audio service settle its new defaults
			if derr == nil {
				changed, err := restoreDefaults(before)
				if err != nil {
					log.Printf("restore default devices: %v", err)
				}
				for _, c := range changed {
					fmt.Println("restored default", c)
					log.Println("restored default", c)
				}
			}
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("%s did not appear; a reboot is usually required after the VB-CABLE driver install", cableEndpoint)
}
