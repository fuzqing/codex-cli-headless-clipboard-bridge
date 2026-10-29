//go:build windows

package main

import "unsafe"

// Windows returns process-external addresses as ULONG_PTR (GlobalLock and hook
// lParam). Reinterpret the ABI word; these pointers never address Go allocations
// and remain valid only for the API's documented lock/callback lifetime.
func nativePointer(word uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&word)) }
