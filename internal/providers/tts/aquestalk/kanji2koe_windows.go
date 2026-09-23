//go:build windows

package aquestalk

import (
	"fmt"
	"syscall"
	"unsafe"
)

const kanji2KoeBufferSize = 8192

type kanji2Koe struct {
	dll     *syscall.LazyDLL
	create  *syscall.LazyProc
	convert *syscall.LazyProc
	release *syscall.LazyProc
	setKey  *syscall.LazyProc

	handle uintptr
}

func newKanji2Koe(
	dllPath string,
	dictionaryPath string,
	devKey string,
) (*kanji2Koe, error) {
	dll := syscall.NewLazyDLL(dllPath)

	k := &kanji2Koe{
		dll:     dll,
		create:  dll.NewProc("AqKanji2Koe_Create"),
		convert: dll.NewProc("AqKanji2Koe_Convert_utf8"),
		release: dll.NewProc("AqKanji2Koe_Release"),
		setKey:  dll.NewProc("AqKanji2Koe_SetDevKey"),
	}

	if err := dll.Load(); err != nil {
		return nil, fmt.Errorf(
			"failed to load AqKanji2Koe DLL %q: %w",
			dllPath,
			err,
		)
	}

	if devKey != "" {
		key, err := syscall.BytePtrFromString(devKey)
		if err != nil {
			return nil, fmt.Errorf(
				"invalid AqKanji2Koe development key: %w",
				err,
			)
		}

		result, _, _ := k.setKey.Call(
			uintptr(unsafe.Pointer(key)),
		)

		if int32(result) != 0 {
			return nil, fmt.Errorf(
				"AqKanji2Koe_SetDevKey failed with code %d",
				int32(result),
			)
		}
	}

	dicPath, err := syscall.BytePtrFromString(dictionaryPath)
	if err != nil {
		return nil, fmt.Errorf(
			"invalid AqKanji2Koe dictionary path: %w",
			err,
		)
	}

	var createErr int32

	handle, _, _ := k.create.Call(
		uintptr(unsafe.Pointer(dicPath)),
		uintptr(unsafe.Pointer(&createErr)),
	)

	if handle == 0 {
		return nil, fmt.Errorf(
			"AqKanji2Koe_Create failed with error code %d",
			createErr,
		)
	}

	k.handle = handle

	return k, nil
}

func (k *kanji2Koe) Convert(text string) (string, error) {
	if k == nil || k.handle == 0 {
		return "", fmt.Errorf("AqKanji2Koe is not initialized")
	}

	input, err := syscall.BytePtrFromString(text)
	if err != nil {
		return "", fmt.Errorf(
			"invalid AqKanji2Koe input: %w",
			err,
		)
	}

	output := make([]byte, kanji2KoeBufferSize)

	result, _, _ := k.convert.Call(
		k.handle,
		uintptr(unsafe.Pointer(input)),
		uintptr(unsafe.Pointer(&output[0])),
		uintptr(len(output)),
	)

	code := int32(result)
	if code != 0 {
		return "", fmt.Errorf(
			"AqKanji2Koe_Convert_utf8 failed with error code %d",
			code,
		)
	}

	end := 0
	for end < len(output) && output[end] != 0 {
		end++
	}

	if end == len(output) {
		return "", fmt.Errorf(
			"AqKanji2Koe output was not null-terminated",
		)
	}

	return string(output[:end]), nil
}

func (k *kanji2Koe) Close() {
	if k == nil || k.handle == 0 {
		return
	}

	k.release.Call(k.handle)
	k.handle = 0
}
