package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	runKeyName = "MaxMix"
	regSZ      = 1
)

var (
	advapi32           = syscall.NewLazyDLL("advapi32.dll")
	procRegSetValueEx  = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValue = advapi32.NewProc("RegDeleteValueW")
)

// setAutostart registers (or removes) a per-user Run entry that starts the
// mixer minimised at logon.
func setAutostart(enable bool) error {
	path, _ := syscall.UTF16PtrFromString(runKeyPath)
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, path, 0, syscall.KEY_SET_VALUE|syscall.KEY_QUERY_VALUE, &key); err != nil {
		return fmt.Errorf("RegOpenKeyEx(%s): %w", runKeyPath, err)
	}
	defer syscall.RegCloseKey(key)
	name, _ := syscall.UTF16PtrFromString(runKeyName)
	if !enable {
		r, _, _ := procRegDeleteValue.Call(uintptr(key), uintptr(unsafe.Pointer(name)))
		if r != 0 && syscall.Errno(r) != syscall.ERROR_FILE_NOT_FOUND {
			return fmt.Errorf("RegDeleteValue: %w", syscall.Errno(r))
		}
		fmt.Println("autostart removed")
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf(`"%s" run`, exe)
	val, err := syscall.UTF16FromString(cmd)
	if err != nil {
		return err
	}
	r, _, _ := procRegSetValueEx.Call(uintptr(key), uintptr(unsafe.Pointer(name)), 0, regSZ, uintptr(unsafe.Pointer(&val[0])), uintptr(len(val)*2))
	if r != 0 {
		return errors.Join(fmt.Errorf("RegSetValueEx"), syscall.Errno(r))
	}
	fmt.Printf("autostart registered: %s\n", cmd)
	return nil
}

// runKeyExists reports whether the autostart Run entry is present.
func runKeyExists() bool {
	path, _ := syscall.UTF16PtrFromString(runKeyPath)
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, path, 0, syscall.KEY_QUERY_VALUE, &key); err != nil {
		return false
	}
	defer syscall.RegCloseKey(key)
	name, _ := syscall.UTF16PtrFromString(runKeyName)
	var typ, size uint32
	return syscall.RegQueryValueEx(key, name, nil, &typ, nil, &size) == nil
}
