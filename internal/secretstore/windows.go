//go:build windows

package secretstore

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

const (
	credTypeGeneric                       = 1
	credPersistLocalMachine               = 2
	errorNotFound           syscall.Errno = 1168
)

var (
	advapi32        = syscall.NewLazyDLL("advapi32.dll")
	procCredReadW   = advapi32.NewProc("CredReadW")
	procCredWriteW  = advapi32.NewProc("CredWriteW")
	procCredDeleteW = advapi32.NewProc("CredDeleteW")
	procCredFree    = advapi32.NewProc("CredFree")
)

type filetime struct{ Low, High uint32 }
type credential struct {
	Flags, Type             uint32
	TargetName, Comment     *uint16
	LastWritten             filetime
	CredentialBlobSize      uint32
	CredentialBlob          *byte
	Persist, AttributeCount uint32
	Attributes              uintptr
	TargetAlias, UserName   *uint16
}

// WindowsStore uses generic credentials scoped to the current Windows account.
type WindowsStore struct{ prefix string }

func NewWindowsStore() *WindowsStore { return &WindowsStore{prefix: "Relay"} }
func newPlatformStore() Store        { return NewWindowsStore() }

func (s *WindowsStore) target(name string) (*uint16, error) {
	if !validName(name) {
		return nil, fmt.Errorf("invalid secret name")
	}
	return syscall.UTF16PtrFromString(s.prefix + "/" + strings.ToUpper(name))
}

func (s *WindowsStore) Get(name string) (string, error) {
	target, err := s.target(name)
	if err != nil {
		return "", err
	}
	var ptr unsafe.Pointer
	r, _, callErr := procCredReadW.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&ptr)))
	if r == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno == errorNotFound {
			return "", ErrNotFound
		}
		return "", callErr
	}
	defer procCredFree.Call(uintptr(ptr))
	c := (*credential)(ptr)
	if c.CredentialBlobSize == 0 {
		return "", nil
	}
	return string(unsafe.Slice(c.CredentialBlob, int(c.CredentialBlobSize))), nil
}

func (s *WindowsStore) Set(name, value string) error {
	target, err := s.target(name)
	if err != nil {
		return err
	}
	blob := []byte(value)
	if len(blob) > 5*512 {
		return fmt.Errorf("secret value exceeds Windows Credential Manager's 2560-byte limit")
	}
	c := credential{Type: credTypeGeneric, TargetName: target, CredentialBlobSize: uint32(len(blob)), Persist: credPersistLocalMachine}
	if len(blob) > 0 {
		c.CredentialBlob = &blob[0]
	}
	r, _, callErr := procCredWriteW.Call(uintptr(unsafe.Pointer(&c)), 0)
	if r == 0 {
		return callErr
	}
	return nil
}

func (s *WindowsStore) Delete(name string) error {
	target, err := s.target(name)
	if err != nil {
		return err
	}
	r, _, callErr := procCredDeleteW.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0)
	if r == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno == errorNotFound {
			return ErrNotFound
		}
		return callErr
	}
	return nil
}
