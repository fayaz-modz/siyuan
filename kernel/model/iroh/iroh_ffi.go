package iroh_ffi

// #cgo CFLAGS: -I${SRCDIR}
// #cgo !android LDFLAGS: -L${SRCDIR} -liroh_ffi -Wl,-rpath=${SRCDIR}
// #cgo android LDFLAGS: -L${SRCDIR} -liroh_ffi -lm -ldl
// #include <iroh_ffi.h>
import "C"

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"runtime"
	"runtime/cgo"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// This is needed, because as of go 1.24
// type RustBuffer C.RustBuffer cannot have methods,
// RustBuffer is treated as non-local type
type GoRustBuffer struct {
	inner C.RustBuffer
}

type RustBufferI interface {
	AsReader() *bytes.Reader
	Free()
	ToGoBytes() []byte
	Data() unsafe.Pointer
	Len() uint64
	Capacity() uint64
}

func RustBufferFromExternal(b RustBufferI) GoRustBuffer {
	return GoRustBuffer{
		inner: C.RustBuffer{
			capacity: C.uint64_t(b.Capacity()),
			len:      C.uint64_t(b.Len()),
			data:     (*C.uchar)(b.Data()),
		},
	}
}

func (cb GoRustBuffer) Capacity() uint64 {
	return uint64(cb.inner.capacity)
}

func (cb GoRustBuffer) Len() uint64 {
	return uint64(cb.inner.len)
}

func (cb GoRustBuffer) Data() unsafe.Pointer {
	return unsafe.Pointer(cb.inner.data)
}

func (cb GoRustBuffer) AsReader() *bytes.Reader {
	b := unsafe.Slice((*byte)(cb.inner.data), C.uint64_t(cb.inner.len))
	return bytes.NewReader(b)
}

func (cb GoRustBuffer) Free() {
	rustCall(func(status *C.RustCallStatus) bool {
		C.ffi_iroh_ffi_rustbuffer_free(cb.inner, status)
		return false
	})
}

func (cb GoRustBuffer) ToGoBytes() []byte {
	return C.GoBytes(unsafe.Pointer(cb.inner.data), C.int(cb.inner.len))
}

func stringToRustBuffer(str string) C.RustBuffer {
	return bytesToRustBuffer([]byte(str))
}

func bytesToRustBuffer(b []byte) C.RustBuffer {
	if len(b) == 0 {
		return C.RustBuffer{}
	}
	// We can pass the pointer along here, as it is pinned
	// for the duration of this call
	foreign := C.ForeignBytes{
		len:  C.int(len(b)),
		data: (*C.uchar)(unsafe.Pointer(&b[0])),
	}

	return rustCall(func(status *C.RustCallStatus) C.RustBuffer {
		return C.ffi_iroh_ffi_rustbuffer_from_bytes(foreign, status)
	})
}

type BufLifter[GoType any] interface {
	Lift(value RustBufferI) GoType
}

type BufLowerer[GoType any] interface {
	Lower(value GoType) C.RustBuffer
}

type BufReader[GoType any] interface {
	Read(reader io.Reader) GoType
}

type BufWriter[GoType any] interface {
	Write(writer io.Writer, value GoType)
}

func LowerIntoRustBuffer[GoType any](bufWriter BufWriter[GoType], value GoType) C.RustBuffer {
	// This might be not the most efficient way but it does not require knowing allocation size
	// beforehand
	var buffer bytes.Buffer
	bufWriter.Write(&buffer, value)

	bytes, err := io.ReadAll(&buffer)
	if err != nil {
		panic(fmt.Errorf("reading written data: %w", err))
	}
	return bytesToRustBuffer(bytes)
}

func LiftFromRustBuffer[GoType any](bufReader BufReader[GoType], rbuf RustBufferI) GoType {
	defer rbuf.Free()
	reader := rbuf.AsReader()
	item := bufReader.Read(reader)
	if reader.Len() > 0 {
		// TODO: Remove this
		leftover, _ := io.ReadAll(reader)
		panic(fmt.Errorf("Junk remaining in buffer after lifting: %s", string(leftover)))
	}
	return item
}

func rustCallWithError[E any, U any](converter BufReader[*E], callback func(*C.RustCallStatus) U) (U, *E) {
	var status C.RustCallStatus
	returnValue := callback(&status)
	err := checkCallStatus(converter, status)
	return returnValue, err
}

func checkCallStatus[E any](converter BufReader[*E], status C.RustCallStatus) *E {
	switch status.code {
	case 0:
		return nil
	case 1:
		return LiftFromRustBuffer(converter, GoRustBuffer{inner: status.errorBuf})
	case 2:
		// when the rust code sees a panic, it tries to construct a rustBuffer
		// with the message.  but if that code panics, then it just sends back
		// an empty buffer.
		if status.errorBuf.len > 0 {
			panic(fmt.Errorf("%s", FfiConverterStringINSTANCE.Lift(GoRustBuffer{inner: status.errorBuf})))
		} else {
			panic(fmt.Errorf("Rust panicked while handling Rust panic"))
		}
	default:
		panic(fmt.Errorf("unknown status code: %d", status.code))
	}
}

func checkCallStatusUnknown(status C.RustCallStatus) error {
	switch status.code {
	case 0:
		return nil
	case 1:
		panic(fmt.Errorf("function not returning an error returned an error"))
	case 2:
		// when the rust code sees a panic, it tries to construct a C.RustBuffer
		// with the message.  but if that code panics, then it just sends back
		// an empty buffer.
		if status.errorBuf.len > 0 {
			panic(fmt.Errorf("%s", FfiConverterStringINSTANCE.Lift(GoRustBuffer{
				inner: status.errorBuf,
			})))
		} else {
			panic(fmt.Errorf("Rust panicked while handling Rust panic"))
		}
	default:
		return fmt.Errorf("unknown status code: %d", status.code)
	}
}

func rustCall[U any](callback func(*C.RustCallStatus) U) U {
	returnValue, err := rustCallWithError[error](nil, callback)
	if err != nil {
		panic(err)
	}
	return returnValue
}

type NativeError interface {
	AsError() error
}

func writeInt8(writer io.Writer, value int8) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint8(writer io.Writer, value uint8) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeInt16(writer io.Writer, value int16) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint16(writer io.Writer, value uint16) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeInt32(writer io.Writer, value int32) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint32(writer io.Writer, value uint32) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeInt64(writer io.Writer, value int64) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint64(writer io.Writer, value uint64) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeFloat32(writer io.Writer, value float32) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeFloat64(writer io.Writer, value float64) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func readInt8(reader io.Reader) int8 {
	var result int8
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint8(reader io.Reader) uint8 {
	var result uint8
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readInt16(reader io.Reader) int16 {
	var result int16
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint16(reader io.Reader) uint16 {
	var result uint16
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readInt32(reader io.Reader) int32 {
	var result int32
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint32(reader io.Reader) uint32 {
	var result uint32
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readInt64(reader io.Reader) int64 {
	var result int64
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint64(reader io.Reader) uint64 {
	var result uint64
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readFloat32(reader io.Reader) float32 {
	var result float32
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readFloat64(reader io.Reader) float64 {
	var result float64
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func init() {

	FfiConverterAddCallbackINSTANCE.register()
	FfiConverterBlobProvideEventCallbackINSTANCE.register()
	FfiConverterDocExportFileCallbackINSTANCE.register()
	FfiConverterDocImportFileCallbackINSTANCE.register()
	FfiConverterDownloadCallbackINSTANCE.register()
	FfiConverterGossipMessageCallbackINSTANCE.register()
	FfiConverterProtocolCreatorINSTANCE.register()
	FfiConverterProtocolHandlerINSTANCE.register()
	FfiConverterSubscribeCallbackINSTANCE.register()
	uniffiCheckChecksums()
}

func uniffiCheckChecksums() {
	// Get the bindings contract version from our ComponentInterface
	bindingsContractVersion := 26
	// Get the scaffolding contract version by calling the into the dylib
	scaffoldingContractVersion := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint32_t {
		return C.ffi_iroh_ffi_uniffi_contract_version()
	})
	if bindingsContractVersion != int(scaffoldingContractVersion) {
		// If this happens try cleaning and rebuilding your project
		panic("iroh_ffi: UniFFI contract version mismatch")
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_func_key_to_path()
		})
		if checksum != 28001 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_func_key_to_path: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_func_path_to_key()
		})
		if checksum != 4438 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_func_path_to_key: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_func_set_log_level()
		})
		if checksum != 52619 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_func_set_log_level: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_addcallback_progress()
		})
		if checksum != 62116 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_addcallback_progress: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_addprogress_as_abort()
		})
		if checksum != 44667 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_addprogress_as_abort: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_addprogress_as_all_done()
		})
		if checksum != 62551 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_addprogress_as_all_done: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_addprogress_as_done()
		})
		if checksum != 58505 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_addprogress_as_done: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_addprogress_as_found()
		})
		if checksum != 8172 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_addprogress_as_found: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_addprogress_as_progress()
		})
		if checksum != 36155 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_addprogress_as_progress: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_addprogress_type()
		})
		if checksum != 46221 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_addprogress_type: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_author_id()
		})
		if checksum != 39022 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_author_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_authorid_equal()
		})
		if checksum != 56356 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_authorid_equal: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_authors_create()
		})
		if checksum != 47692 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_authors_create: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_authors_default()
		})
		if checksum != 6795 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_authors_default: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_authors_delete()
		})
		if checksum != 51040 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_authors_delete: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_authors_export()
		})
		if checksum != 17391 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_authors_export: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_authors_import()
		})
		if checksum != 11067 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_authors_import: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_authors_import_author()
		})
		if checksum != 56460 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_authors_import_author: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_authors_list()
		})
		if checksum != 33930 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_authors_list: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_bistream_recv()
		})
		if checksum != 60625 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_bistream_recv: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_bistream_send()
		})
		if checksum != 13146 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_bistream_send: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobprovideevent_as_client_connected()
		})
		if checksum != 48446 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobprovideevent_as_client_connected: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobprovideevent_as_get_request_received()
		})
		if checksum != 8740 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobprovideevent_as_get_request_received: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobprovideevent_as_tagged_blob_added()
		})
		if checksum != 59887 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobprovideevent_as_tagged_blob_added: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobprovideevent_as_transfer_aborted()
		})
		if checksum != 41238 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobprovideevent_as_transfer_aborted: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobprovideevent_as_transfer_blob_completed()
		})
		if checksum != 20663 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobprovideevent_as_transfer_blob_completed: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobprovideevent_as_transfer_completed()
		})
		if checksum != 47368 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobprovideevent_as_transfer_completed: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobprovideevent_as_transfer_hash_seq_started()
		})
		if checksum != 27778 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobprovideevent_as_transfer_hash_seq_started: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobprovideevent_as_transfer_progress()
		})
		if checksum != 40626 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobprovideevent_as_transfer_progress: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobprovideevent_type()
		})
		if checksum != 51159 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobprovideevent_type: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobprovideeventcallback_blob_event()
		})
		if checksum != 43399 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobprovideeventcallback_blob_event: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobticket_as_download_options()
		})
		if checksum != 18713 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobticket_as_download_options: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobticket_format()
		})
		if checksum != 35808 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobticket_format: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobticket_hash()
		})
		if checksum != 54061 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobticket_hash: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobticket_node_addr()
		})
		if checksum != 30662 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobticket_node_addr: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobticket_recursive()
		})
		if checksum != 53797 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobticket_recursive: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_add_bytes()
		})
		if checksum != 16525 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_add_bytes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_add_bytes_named()
		})
		if checksum != 4623 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_add_bytes_named: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_add_from_path()
		})
		if checksum != 12412 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_add_from_path: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_create_collection()
		})
		if checksum != 63440 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_create_collection: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_delete_blob()
		})
		if checksum != 24901 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_delete_blob: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_download()
		})
		if checksum != 14779 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_download: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_export()
		})
		if checksum != 23697 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_export: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_get_collection()
		})
		if checksum != 57130 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_get_collection: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_has()
		})
		if checksum != 1301 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_has: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_list()
		})
		if checksum != 9714 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_list: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_list_collections()
		})
		if checksum != 22274 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_list_collections: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_list_incomplete()
		})
		if checksum != 31740 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_list_incomplete: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_read_at_to_bytes()
		})
		if checksum != 43209 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_read_at_to_bytes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_read_to_bytes()
		})
		if checksum != 13624 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_read_to_bytes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_share()
		})
		if checksum != 35831 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_share: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_size()
		})
		if checksum != 20254 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_size: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_status()
		})
		if checksum != 34093 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_status: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_blobs_write_to_path()
		})
		if checksum != 47517 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_blobs_write_to_path: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_collection_blobs()
		})
		if checksum != 52509 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_collection_blobs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_collection_is_empty()
		})
		if checksum != 40621 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_collection_is_empty: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_collection_len()
		})
		if checksum != 10206 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_collection_len: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_collection_links()
		})
		if checksum != 56034 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_collection_links: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_collection_names()
		})
		if checksum != 28871 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_collection_names: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_collection_push()
		})
		if checksum != 22031 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_collection_push: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connecting_alpn()
		})
		if checksum != 45347 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connecting_alpn: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connecting_connect()
		})
		if checksum != 64341 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connecting_connect: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_accept_bi()
		})
		if checksum != 10996 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_accept_bi: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_accept_uni()
		})
		if checksum != 17891 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_accept_uni: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_alpn()
		})
		if checksum != 53975 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_alpn: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_close()
		})
		if checksum != 61009 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_close: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_close_reason()
		})
		if checksum != 44737 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_close_reason: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_closed()
		})
		if checksum != 30404 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_closed: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_datagram_send_buffer_space()
		})
		if checksum != 52904 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_datagram_send_buffer_space: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_max_datagram_size()
		})
		if checksum != 49257 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_max_datagram_size: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_open_bi()
		})
		if checksum != 34801 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_open_bi: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_open_uni()
		})
		if checksum != 36079 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_open_uni: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_read_datagram()
		})
		if checksum != 23201 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_read_datagram: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_remote_node_id()
		})
		if checksum != 59577 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_remote_node_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_rtt()
		})
		if checksum != 61654 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_rtt: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_send_datagram()
		})
		if checksum != 105 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_send_datagram: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_set_max_concurrent_bii_stream()
		})
		if checksum != 13576 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_set_max_concurrent_bii_stream: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_set_max_concurrent_uni_stream()
		})
		if checksum != 26642 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_set_max_concurrent_uni_stream: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_set_receive_window()
		})
		if checksum != 27731 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_set_receive_window: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connection_stable_id()
		})
		if checksum != 28186 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connection_stable_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connectiontype_as_direct()
		})
		if checksum != 47530 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connectiontype_as_direct: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connectiontype_as_mixed()
		})
		if checksum != 49068 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connectiontype_as_mixed: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connectiontype_as_relay()
		})
		if checksum != 6121 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connectiontype_as_relay: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_connectiontype_type()
		})
		if checksum != 54998 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_connectiontype_type: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_directaddrinfo_addr()
		})
		if checksum != 20100 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_directaddrinfo_addr: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_directaddrinfo_last_control()
		})
		if checksum != 35048 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_directaddrinfo_last_control: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_directaddrinfo_last_payload()
		})
		if checksum != 12406 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_directaddrinfo_last_payload: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_directaddrinfo_latency()
		})
		if checksum != 7414 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_directaddrinfo_latency: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_close_me()
		})
		if checksum != 13449 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_close_me: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_delete()
		})
		if checksum != 54552 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_delete: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_export_file()
		})
		if checksum != 16067 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_export_file: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_get_download_policy()
		})
		if checksum != 44884 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_get_download_policy: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_get_exact()
		})
		if checksum != 20423 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_get_exact: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_get_many()
		})
		if checksum != 53909 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_get_many: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_get_one()
		})
		if checksum != 18797 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_get_one: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_get_sync_peers()
		})
		if checksum != 59505 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_get_sync_peers: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_id()
		})
		if checksum != 53450 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_import_file()
		})
		if checksum != 52327 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_import_file: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_leave()
		})
		if checksum != 40204 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_leave: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_set_bytes()
		})
		if checksum != 32483 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_set_bytes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_set_download_policy()
		})
		if checksum != 18200 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_set_download_policy: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_set_hash()
		})
		if checksum != 30875 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_set_hash: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_share()
		})
		if checksum != 59706 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_share: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_start_sync()
		})
		if checksum != 54450 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_start_sync: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_status()
		})
		if checksum != 30558 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_status: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_doc_subscribe()
		})
		if checksum != 59807 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_doc_subscribe: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docexportfilecallback_progress()
		})
		if checksum != 53186 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docexportfilecallback_progress: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docexportprogress_as_abort()
		})
		if checksum != 34476 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docexportprogress_as_abort: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docexportprogress_as_found()
		})
		if checksum != 23982 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docexportprogress_as_found: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docexportprogress_as_progress()
		})
		if checksum != 44802 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docexportprogress_as_progress: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docexportprogress_type()
		})
		if checksum != 11215 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docexportprogress_type: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docimportfilecallback_progress()
		})
		if checksum != 55347 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docimportfilecallback_progress: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docimportprogress_as_abort()
		})
		if checksum != 35952 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docimportprogress_as_abort: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docimportprogress_as_all_done()
		})
		if checksum != 35787 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docimportprogress_as_all_done: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docimportprogress_as_found()
		})
		if checksum != 6030 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docimportprogress_as_found: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docimportprogress_as_ingest_done()
		})
		if checksum != 36 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docimportprogress_as_ingest_done: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docimportprogress_as_progress()
		})
		if checksum != 19927 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docimportprogress_as_progress: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docimportprogress_type()
		})
		if checksum != 48401 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docimportprogress_type: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docs_create()
		})
		if checksum != 54486 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docs_create: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docs_drop_doc()
		})
		if checksum != 5864 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docs_drop_doc: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docs_join()
		})
		if checksum != 38489 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docs_join: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docs_join_and_subscribe()
		})
		if checksum != 41379 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docs_join_and_subscribe: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docs_list()
		})
		if checksum != 23866 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docs_list: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_docs_open()
		})
		if checksum != 45928 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_docs_open: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_downloadcallback_progress()
		})
		if checksum != 21881 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_downloadcallback_progress: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_downloadprogress_as_abort()
		})
		if checksum != 6879 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_downloadprogress_as_abort: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_downloadprogress_as_all_done()
		})
		if checksum != 4219 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_downloadprogress_as_all_done: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_downloadprogress_as_done()
		})
		if checksum != 21859 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_downloadprogress_as_done: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_downloadprogress_as_found()
		})
		if checksum != 47836 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_downloadprogress_as_found: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_downloadprogress_as_found_hash_seq()
		})
		if checksum != 14451 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_downloadprogress_as_found_hash_seq: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_downloadprogress_as_found_local()
		})
		if checksum != 47262 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_downloadprogress_as_found_local: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_downloadprogress_as_progress()
		})
		if checksum != 16155 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_downloadprogress_as_progress: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_downloadprogress_type()
		})
		if checksum != 60534 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_downloadprogress_type: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_endpoint_connect()
		})
		if checksum != 29734 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_endpoint_connect: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_endpoint_node_id()
		})
		if checksum != 54517 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_endpoint_node_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_entry_author()
		})
		if checksum != 39787 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_entry_author: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_entry_content_hash()
		})
		if checksum != 26949 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_entry_content_hash: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_entry_content_len()
		})
		if checksum != 40073 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_entry_content_len: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_entry_key()
		})
		if checksum != 10200 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_entry_key: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_entry_namespace()
		})
		if checksum != 25213 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_entry_namespace: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_entry_timestamp()
		})
		if checksum != 38377 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_entry_timestamp: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_filterkind_matches()
		})
		if checksum != 24522 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_filterkind_matches: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_gossip_subscribe()
		})
		if checksum != 6414 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_gossip_subscribe: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_gossipmessagecallback_on_message()
		})
		if checksum != 49150 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_gossipmessagecallback_on_message: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_hash_equal()
		})
		if checksum != 28210 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_hash_equal: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_hash_to_bytes()
		})
		if checksum != 26394 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_hash_to_bytes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_hash_to_hex()
		})
		if checksum != 52108 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_hash_to_hex: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_iroh_authors()
		})
		if checksum != 61389 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_iroh_authors: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_iroh_blobs()
		})
		if checksum != 50340 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_iroh_blobs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_iroh_docs()
		})
		if checksum != 17607 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_iroh_docs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_iroh_gossip()
		})
		if checksum != 58884 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_iroh_gossip: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_iroh_net()
		})
		if checksum != 41953 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_iroh_net: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_iroh_node()
		})
		if checksum != 12499 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_iroh_node: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_iroh_tags()
		})
		if checksum != 59606 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_iroh_tags: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_iroherror_message()
		})
		if checksum != 31085 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_iroherror_message: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_liveevent_as_content_ready()
		})
		if checksum != 6578 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_liveevent_as_content_ready: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_liveevent_as_insert_local()
		})
		if checksum != 27496 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_liveevent_as_insert_local: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_liveevent_as_insert_remote()
		})
		if checksum != 38454 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_liveevent_as_insert_remote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_liveevent_as_neighbor_down()
		})
		if checksum != 27752 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_liveevent_as_neighbor_down: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_liveevent_as_neighbor_up()
		})
		if checksum != 44203 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_liveevent_as_neighbor_up: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_liveevent_as_sync_finished()
		})
		if checksum != 27893 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_liveevent_as_sync_finished: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_liveevent_type()
		})
		if checksum != 30099 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_liveevent_type: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_message_as_error()
		})
		if checksum != 9059 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_message_as_error: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_message_as_joined()
		})
		if checksum != 39463 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_message_as_joined: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_message_as_neighbor_down()
		})
		if checksum != 19092 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_message_as_neighbor_down: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_message_as_neighbor_up()
		})
		if checksum != 3541 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_message_as_neighbor_up: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_message_as_received()
		})
		if checksum != 6044 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_message_as_received: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_message_type()
		})
		if checksum != 75 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_message_type: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_net_add_node_addr()
		})
		if checksum != 17723 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_net_add_node_addr: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_net_home_relay()
		})
		if checksum != 3492 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_net_home_relay: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_net_node_addr()
		})
		if checksum != 60712 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_net_node_addr: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_net_node_id()
		})
		if checksum != 35201 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_net_node_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_net_remote_info()
		})
		if checksum != 60537 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_net_remote_info: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_net_remote_info_list()
		})
		if checksum != 15919 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_net_remote_info_list: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_node_endpoint()
		})
		if checksum != 6829 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_node_endpoint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_node_shutdown()
		})
		if checksum != 49624 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_node_shutdown: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_node_stats()
		})
		if checksum != 13439 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_node_stats: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_node_status()
		})
		if checksum != 21889 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_node_status: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_nodeaddr_direct_addresses()
		})
		if checksum != 23787 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_nodeaddr_direct_addresses: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_nodeaddr_equal()
		})
		if checksum != 19664 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_nodeaddr_equal: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_nodeaddr_relay_url()
		})
		if checksum != 34772 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_nodeaddr_relay_url: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_nodestatus_listen_addrs()
		})
		if checksum != 54436 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_nodestatus_listen_addrs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_nodestatus_node_addr()
		})
		if checksum != 12507 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_nodestatus_node_addr: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_nodestatus_rpc_addr()
		})
		if checksum != 20002 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_nodestatus_rpc_addr: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_nodestatus_version()
		})
		if checksum != 3183 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_nodestatus_version: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_nodeticket_node_addr()
		})
		if checksum != 3397 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_nodeticket_node_addr: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_protocolcreator_create()
		})
		if checksum != 33391 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_protocolcreator_create: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_protocolhandler_accept()
		})
		if checksum != 45944 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_protocolhandler_accept: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_protocolhandler_shutdown()
		})
		if checksum != 55574 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_protocolhandler_shutdown: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_publickey_equal()
		})
		if checksum != 8690 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_publickey_equal: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_publickey_fmt_short()
		})
		if checksum != 31871 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_publickey_fmt_short: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_publickey_to_bytes()
		})
		if checksum != 22449 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_publickey_to_bytes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_query_limit()
		})
		if checksum != 23235 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_query_limit: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_query_offset()
		})
		if checksum != 14460 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_query_offset: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_rangespec_is_all()
		})
		if checksum != 51737 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_rangespec_is_all: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_rangespec_is_empty()
		})
		if checksum != 38175 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_rangespec_is_empty: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_recvstream_id()
		})
		if checksum != 17291 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_recvstream_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_recvstream_read()
		})
		if checksum != 25331 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_recvstream_read: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_recvstream_read_exact()
		})
		if checksum != 37269 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_recvstream_read_exact: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_recvstream_read_to_end()
		})
		if checksum != 31754 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_recvstream_read_to_end: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_recvstream_received_reset()
		})
		if checksum != 12049 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_recvstream_received_reset: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_recvstream_stop()
		})
		if checksum != 5360 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_recvstream_stop: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_sendstream_finish()
		})
		if checksum != 32400 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_sendstream_finish: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_sendstream_id()
		})
		if checksum != 905 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_sendstream_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_sendstream_priority()
		})
		if checksum != 33897 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_sendstream_priority: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_sendstream_reset()
		})
		if checksum != 34438 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_sendstream_reset: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_sendstream_set_priority()
		})
		if checksum != 1968 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_sendstream_set_priority: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_sendstream_stopped()
		})
		if checksum != 40814 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_sendstream_stopped: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_sendstream_write()
		})
		if checksum != 61923 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_sendstream_write: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_sendstream_write_all()
		})
		if checksum != 5755 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_sendstream_write_all: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_sender_broadcast()
		})
		if checksum != 42694 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_sender_broadcast: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_sender_broadcast_neighbors()
		})
		if checksum != 14000 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_sender_broadcast_neighbors: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_sender_cancel()
		})
		if checksum != 24357 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_sender_cancel: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_subscribecallback_event()
		})
		if checksum != 35520 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_subscribecallback_event: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_tags_delete()
		})
		if checksum != 17755 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_tags_delete: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_method_tags_list()
		})
		if checksum != 16151 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_method_tags_list: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_author_from_string()
		})
		if checksum != 63158 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_author_from_string: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_authorid_from_string()
		})
		if checksum != 47849 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_authorid_from_string: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_blobdownloadoptions_new()
		})
		if checksum != 46030 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_blobdownloadoptions_new: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_blobticket_new()
		})
		if checksum != 29763 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_blobticket_new: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_collection_new()
		})
		if checksum != 3798 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_collection_new: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_docticket_new()
		})
		if checksum != 29537 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_docticket_new: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_downloadpolicy_everything()
		})
		if checksum != 35143 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_downloadpolicy_everything: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_downloadpolicy_everything_except()
		})
		if checksum != 21211 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_downloadpolicy_everything_except: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_downloadpolicy_nothing()
		})
		if checksum != 16928 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_downloadpolicy_nothing: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_downloadpolicy_nothing_except()
		})
		if checksum != 12041 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_downloadpolicy_nothing_except: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_filterkind_exact()
		})
		if checksum != 13432 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_filterkind_exact: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_filterkind_prefix()
		})
		if checksum != 42338 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_filterkind_prefix: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_hash_from_bytes()
		})
		if checksum != 13104 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_hash_from_bytes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_hash_from_string()
		})
		if checksum != 23453 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_hash_from_string: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_hash_new()
		})
		if checksum != 30613 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_hash_new: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_iroh_memory()
		})
		if checksum != 49939 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_iroh_memory: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_iroh_memory_with_options()
		})
		if checksum != 60437 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_iroh_memory_with_options: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_iroh_persistent()
		})
		if checksum != 42623 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_iroh_persistent: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_iroh_persistent_with_options()
		})
		if checksum != 60788 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_iroh_persistent_with_options: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_nodeaddr_new()
		})
		if checksum != 5759 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_nodeaddr_new: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_nodeticket_new()
		})
		if checksum != 8609 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_nodeticket_new: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_nodeticket_parse()
		})
		if checksum != 16834 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_nodeticket_parse: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_publickey_from_bytes()
		})
		if checksum != 64011 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_publickey_from_bytes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_publickey_from_string()
		})
		if checksum != 42207 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_publickey_from_string: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_query_all()
		})
		if checksum != 34328 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_query_all: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_query_author()
		})
		if checksum != 17803 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_query_author: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_query_author_key_exact()
		})
		if checksum != 38571 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_query_author_key_exact: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_query_author_key_prefix()
		})
		if checksum != 48731 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_query_author_key_prefix: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_query_key_exact()
		})
		if checksum != 17481 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_query_key_exact: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_query_key_prefix()
		})
		if checksum != 35279 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_query_key_prefix: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_query_single_latest_per_key()
		})
		if checksum != 58221 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_query_single_latest_per_key: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_query_single_latest_per_key_exact()
		})
		if checksum != 6734 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_query_single_latest_per_key_exact: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_query_single_latest_per_key_prefix()
		})
		if checksum != 8914 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_query_single_latest_per_key_prefix: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_readatlen_all()
		})
		if checksum != 34450 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_readatlen_all: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_readatlen_at_most()
		})
		if checksum != 62414 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_readatlen_at_most: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_readatlen_exact()
		})
		if checksum != 12971 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_readatlen_exact: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_settagoption_auto()
		})
		if checksum != 50496 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_settagoption_auto: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_settagoption_named()
		})
		if checksum != 33009 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_settagoption_named: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_wrapoption_no_wrap()
		})
		if checksum != 59800 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_wrapoption_no_wrap: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_iroh_ffi_checksum_constructor_wrapoption_wrap()
		})
		if checksum != 6667 {
			// If this happens try cleaning and rebuilding your project
			panic("iroh_ffi: uniffi_iroh_ffi_checksum_constructor_wrapoption_wrap: UniFFI API checksum mismatch")
		}
	}
}

type FfiConverterUint32 struct{}

var FfiConverterUint32INSTANCE = FfiConverterUint32{}

func (FfiConverterUint32) Lower(value uint32) C.uint32_t {
	return C.uint32_t(value)
}

func (FfiConverterUint32) Write(writer io.Writer, value uint32) {
	writeUint32(writer, value)
}

func (FfiConverterUint32) Lift(value C.uint32_t) uint32 {
	return uint32(value)
}

func (FfiConverterUint32) Read(reader io.Reader) uint32 {
	return readUint32(reader)
}

type FfiDestroyerUint32 struct{}

func (FfiDestroyerUint32) Destroy(_ uint32) {}

type FfiConverterInt32 struct{}

var FfiConverterInt32INSTANCE = FfiConverterInt32{}

func (FfiConverterInt32) Lower(value int32) C.int32_t {
	return C.int32_t(value)
}

func (FfiConverterInt32) Write(writer io.Writer, value int32) {
	writeInt32(writer, value)
}

func (FfiConverterInt32) Lift(value C.int32_t) int32 {
	return int32(value)
}

func (FfiConverterInt32) Read(reader io.Reader) int32 {
	return readInt32(reader)
}

type FfiDestroyerInt32 struct{}

func (FfiDestroyerInt32) Destroy(_ int32) {}

type FfiConverterUint64 struct{}

var FfiConverterUint64INSTANCE = FfiConverterUint64{}

func (FfiConverterUint64) Lower(value uint64) C.uint64_t {
	return C.uint64_t(value)
}

func (FfiConverterUint64) Write(writer io.Writer, value uint64) {
	writeUint64(writer, value)
}

func (FfiConverterUint64) Lift(value C.uint64_t) uint64 {
	return uint64(value)
}

func (FfiConverterUint64) Read(reader io.Reader) uint64 {
	return readUint64(reader)
}

type FfiDestroyerUint64 struct{}

func (FfiDestroyerUint64) Destroy(_ uint64) {}

type FfiConverterBool struct{}

var FfiConverterBoolINSTANCE = FfiConverterBool{}

func (FfiConverterBool) Lower(value bool) C.int8_t {
	if value {
		return C.int8_t(1)
	}
	return C.int8_t(0)
}

func (FfiConverterBool) Write(writer io.Writer, value bool) {
	if value {
		writeInt8(writer, 1)
	} else {
		writeInt8(writer, 0)
	}
}

func (FfiConverterBool) Lift(value C.int8_t) bool {
	return value != 0
}

func (FfiConverterBool) Read(reader io.Reader) bool {
	return readInt8(reader) != 0
}

type FfiDestroyerBool struct{}

func (FfiDestroyerBool) Destroy(_ bool) {}

type FfiConverterString struct{}

var FfiConverterStringINSTANCE = FfiConverterString{}

func (FfiConverterString) Lift(rb RustBufferI) string {
	defer rb.Free()
	reader := rb.AsReader()
	b, err := io.ReadAll(reader)
	if err != nil {
		panic(fmt.Errorf("reading reader: %w", err))
	}
	return string(b)
}

func (FfiConverterString) Read(reader io.Reader) string {
	length := readInt32(reader)
	buffer := make([]byte, length)
	read_length, err := reader.Read(buffer)
	if err != nil {
		panic(err)
	}
	if read_length != int(length) {
		panic(fmt.Errorf("bad read length when reading string, expected %d, read %d", length, read_length))
	}
	return string(buffer)
}

func (FfiConverterString) Lower(value string) C.RustBuffer {
	return stringToRustBuffer(value)
}

func (FfiConverterString) Write(writer io.Writer, value string) {
	if len(value) > math.MaxInt32 {
		panic("String is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	write_length, err := io.WriteString(writer, value)
	if err != nil {
		panic(err)
	}
	if write_length != len(value) {
		panic(fmt.Errorf("bad write length when writing string, expected %d, written %d", len(value), write_length))
	}
}

type FfiDestroyerString struct{}

func (FfiDestroyerString) Destroy(_ string) {}

type FfiConverterBytes struct{}

var FfiConverterBytesINSTANCE = FfiConverterBytes{}

func (c FfiConverterBytes) Lower(value []byte) C.RustBuffer {
	return LowerIntoRustBuffer[[]byte](c, value)
}

func (c FfiConverterBytes) Write(writer io.Writer, value []byte) {
	if len(value) > math.MaxInt32 {
		panic("[]byte is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	write_length, err := writer.Write(value)
	if err != nil {
		panic(err)
	}
	if write_length != len(value) {
		panic(fmt.Errorf("bad write length when writing []byte, expected %d, written %d", len(value), write_length))
	}
}

func (c FfiConverterBytes) Lift(rb RustBufferI) []byte {
	return LiftFromRustBuffer[[]byte](c, rb)
}

func (c FfiConverterBytes) Read(reader io.Reader) []byte {
	length := readInt32(reader)
	buffer := make([]byte, length)
	read_length, err := reader.Read(buffer)
	if err != nil {
		panic(err)
	}
	if read_length != int(length) {
		panic(fmt.Errorf("bad read length when reading []byte, expected %d, read %d", length, read_length))
	}
	return buffer
}

type FfiDestroyerBytes struct{}

func (FfiDestroyerBytes) Destroy(_ []byte) {}

type FfiConverterTimestamp struct{}

var FfiConverterTimestampINSTANCE = FfiConverterTimestamp{}

func (c FfiConverterTimestamp) Lift(rb RustBufferI) time.Time {
	return LiftFromRustBuffer[time.Time](c, rb)
}

func (c FfiConverterTimestamp) Read(reader io.Reader) time.Time {
	sec := readInt64(reader)
	nsec := readUint32(reader)

	var sign int64 = 1
	if sec < 0 {
		sign = -1
	}

	return time.Unix(sec, int64(nsec)*sign)
}

func (c FfiConverterTimestamp) Lower(value time.Time) C.RustBuffer {
	return LowerIntoRustBuffer[time.Time](c, value)
}

func (c FfiConverterTimestamp) Write(writer io.Writer, value time.Time) {
	sec := value.Unix()
	nsec := uint32(value.Nanosecond())
	if value.Unix() < 0 {
		nsec = 1_000_000_000 - nsec
		sec += 1
	}

	writeInt64(writer, sec)
	writeUint32(writer, nsec)
}

type FfiDestroyerTimestamp struct{}

func (FfiDestroyerTimestamp) Destroy(_ time.Time) {}

// FfiConverterDuration converts between uniffi duration and Go duration.
type FfiConverterDuration struct{}

var FfiConverterDurationINSTANCE = FfiConverterDuration{}

func (c FfiConverterDuration) Lift(rb RustBufferI) time.Duration {
	return LiftFromRustBuffer[time.Duration](c, rb)
}

func (c FfiConverterDuration) Read(reader io.Reader) time.Duration {
	sec := readUint64(reader)
	nsec := readUint32(reader)
	return time.Duration(sec*1_000_000_000 + uint64(nsec))
}

func (c FfiConverterDuration) Lower(value time.Duration) C.RustBuffer {
	return LowerIntoRustBuffer[time.Duration](c, value)
}

func (c FfiConverterDuration) Write(writer io.Writer, value time.Duration) {
	if value.Nanoseconds() < 0 {
		// Rust does not support negative durations:
		// https://www.reddit.com/r/rust/comments/ljl55u/why_rusts_duration_not_supporting_negative_values/
		// This panic is very bad, because it depends on user input, and in Go user input related
		// error are supposed to be returned as errors, and not cause panics. However, with the
		// current architecture, its not possible to return an error from here, so panic is used as
		// the only other option to signal an error.
		panic("negative duration is not allowed")
	}

	writeUint64(writer, uint64(value)/1_000_000_000)
	writeUint32(writer, uint32(uint64(value)%1_000_000_000))
}

type FfiDestroyerDuration struct{}

func (FfiDestroyerDuration) Destroy(_ time.Duration) {}

// Below is an implementation of synchronization requirements outlined in the link.
// https://github.com/mozilla/uniffi-rs/blob/0dc031132d9493ca812c3af6e7dd60ad2ea95bf0/uniffi_bindgen/src/bindings/kotlin/templates/ObjectRuntime.kt#L31

type FfiObject struct {
	pointer       unsafe.Pointer
	callCounter   atomic.Int64
	cloneFunction func(unsafe.Pointer, *C.RustCallStatus) unsafe.Pointer
	freeFunction  func(unsafe.Pointer, *C.RustCallStatus)
	destroyed     atomic.Bool
}

func newFfiObject(
	pointer unsafe.Pointer,
	cloneFunction func(unsafe.Pointer, *C.RustCallStatus) unsafe.Pointer,
	freeFunction func(unsafe.Pointer, *C.RustCallStatus),
) FfiObject {
	return FfiObject{
		pointer:       pointer,
		cloneFunction: cloneFunction,
		freeFunction:  freeFunction,
	}
}

func (ffiObject *FfiObject) incrementPointer(debugName string) unsafe.Pointer {
	for {
		counter := ffiObject.callCounter.Load()
		if counter <= -1 {
			panic(fmt.Errorf("%v object has already been destroyed", debugName))
		}
		if counter == math.MaxInt64 {
			panic(fmt.Errorf("%v object call counter would overflow", debugName))
		}
		if ffiObject.callCounter.CompareAndSwap(counter, counter+1) {
			break
		}
	}

	return rustCall(func(status *C.RustCallStatus) unsafe.Pointer {
		return ffiObject.cloneFunction(ffiObject.pointer, status)
	})
}

func (ffiObject *FfiObject) decrementPointer() {
	if ffiObject.callCounter.Add(-1) == -1 {
		ffiObject.freeRustArcPtr()
	}
}

func (ffiObject *FfiObject) destroy() {
	if ffiObject.destroyed.CompareAndSwap(false, true) {
		if ffiObject.callCounter.Add(-1) == -1 {
			ffiObject.freeRustArcPtr()
		}
	}
}

func (ffiObject *FfiObject) freeRustArcPtr() {
	rustCall(func(status *C.RustCallStatus) int32 {
		ffiObject.freeFunction(ffiObject.pointer, status)
		return 0
	})
}

// The `progress` method will be called for each `AddProgress` event that is
// emitted during a `node.blobs_add_from_path`. Use the `AddProgress.type()`
// method to check the `AddProgressType`
type AddCallback interface {
	Progress(progress *AddProgress) *CallbackError
}

// The `progress` method will be called for each `AddProgress` event that is
// emitted during a `node.blobs_add_from_path`. Use the `AddProgress.type()`
// method to check the `AddProgressType`
type AddCallbackImpl struct {
	ffiObject FfiObject
}

func (_self *AddCallbackImpl) Progress(progress *AddProgress) *CallbackError {
	_pointer := _self.ffiObject.incrementPointer("AddCallback")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[CallbackError](
		FfiConverterCallbackErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_addcallback_progress(
			_pointer, FfiConverterAddProgressINSTANCE.Lower(progress)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}
func (object *AddCallbackImpl) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterAddCallback struct {
	handleMap *concurrentHandleMap[AddCallback]
}

var FfiConverterAddCallbackINSTANCE = FfiConverterAddCallback{
	handleMap: newConcurrentHandleMap[AddCallback](),
}

func (c FfiConverterAddCallback) Lift(pointer unsafe.Pointer) AddCallback {
	result := &AddCallbackImpl{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_addcallback(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_addcallback(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*AddCallbackImpl).Destroy)
	return result
}

func (c FfiConverterAddCallback) Read(reader io.Reader) AddCallback {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterAddCallback) Lower(value AddCallback) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := unsafe.Pointer(uintptr(c.handleMap.insert(value)))
	return pointer

}

func (c FfiConverterAddCallback) Write(writer io.Writer, value AddCallback) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerAddCallback struct{}

func (_ FfiDestroyerAddCallback) Destroy(value AddCallback) {
	if val, ok := value.(*AddCallbackImpl); ok {
		val.Destroy()
	} else {
		panic("Expected *AddCallbackImpl")
	}
}

type uniffiCallbackResult C.int8_t

const (
	uniffiIdxCallbackFree               uniffiCallbackResult = 0
	uniffiCallbackResultSuccess         uniffiCallbackResult = 0
	uniffiCallbackResultError           uniffiCallbackResult = 1
	uniffiCallbackUnexpectedResultError uniffiCallbackResult = 2
	uniffiCallbackCancelled             uniffiCallbackResult = 3
)

type concurrentHandleMap[T any] struct {
	handles       map[uint64]T
	currentHandle uint64
	lock          sync.RWMutex
}

func newConcurrentHandleMap[T any]() *concurrentHandleMap[T] {
	return &concurrentHandleMap[T]{
		handles: map[uint64]T{},
	}
}

func (cm *concurrentHandleMap[T]) insert(obj T) uint64 {
	cm.lock.Lock()
	defer cm.lock.Unlock()

	cm.currentHandle = cm.currentHandle + 1
	cm.handles[cm.currentHandle] = obj
	return cm.currentHandle
}

func (cm *concurrentHandleMap[T]) remove(handle uint64) {
	cm.lock.Lock()
	defer cm.lock.Unlock()

	delete(cm.handles, handle)
}

func (cm *concurrentHandleMap[T]) tryGet(handle uint64) (T, bool) {
	cm.lock.RLock()
	defer cm.lock.RUnlock()

	val, ok := cm.handles[handle]
	return val, ok
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceAddCallbackMethod0
func iroh_ffi_cgo_dispatchCallbackInterfaceAddCallbackMethod0(uniffiHandle C.uint64_t, progress unsafe.Pointer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterAddCallbackINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.iroh_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.Progress(
				FfiConverterAddProgressINSTANCE.Lift(progress),
			)

		if err != nil {
			// The only way to bypass an unexpected error is to bypass pointer to an empty
			// instance of the error
			if err.err == nil {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
				return
			}

			*callStatus = C.RustCallStatus{
				code:     C.int8_t(uniffiCallbackResultError),
				errorBuf: FfiConverterCallbackErrorINSTANCE.Lower(err),
			}
			return
		}

	}()
}

var UniffiVTableCallbackInterfaceAddCallbackINSTANCE = C.UniffiVTableCallbackInterfaceAddCallback{
	progress: (C.UniffiCallbackInterfaceAddCallbackMethod0)(C.iroh_ffi_cgo_dispatchCallbackInterfaceAddCallbackMethod0),

	uniffiFree: (C.UniffiCallbackInterfaceFree)(C.iroh_ffi_cgo_dispatchCallbackInterfaceAddCallbackFree),
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceAddCallbackFree
func iroh_ffi_cgo_dispatchCallbackInterfaceAddCallbackFree(handle C.uint64_t) {
	FfiConverterAddCallbackINSTANCE.handleMap.remove(uint64(handle))
}

func (c FfiConverterAddCallback) register() {
	C.uniffi_iroh_ffi_fn_init_callback_vtable_addcallback(&UniffiVTableCallbackInterfaceAddCallbackINSTANCE)
}

// Progress updates for the add operation.
type AddProgressInterface interface {
	// Return the `AddProgressAbort`
	AsAbort() AddProgressAbort
	// Return the `AddAllDone`
	AsAllDone() AddProgressAllDone
	// Return the `AddProgressDone` event
	AsDone() AddProgressDone
	// Return the `AddProgressFound` event
	AsFound() AddProgressFound
	// Return the `AddProgressProgress` event
	AsProgress() AddProgressProgress
	// Get the type of event
	Type() AddProgressType
}

// Progress updates for the add operation.
type AddProgress struct {
	ffiObject FfiObject
}

// Return the `AddProgressAbort`
func (_self *AddProgress) AsAbort() AddProgressAbort {
	_pointer := _self.ffiObject.incrementPointer("*AddProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterAddProgressAbortINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_addprogress_as_abort(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `AddAllDone`
func (_self *AddProgress) AsAllDone() AddProgressAllDone {
	_pointer := _self.ffiObject.incrementPointer("*AddProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterAddProgressAllDoneINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_addprogress_as_all_done(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `AddProgressDone` event
func (_self *AddProgress) AsDone() AddProgressDone {
	_pointer := _self.ffiObject.incrementPointer("*AddProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterAddProgressDoneINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_addprogress_as_done(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `AddProgressFound` event
func (_self *AddProgress) AsFound() AddProgressFound {
	_pointer := _self.ffiObject.incrementPointer("*AddProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterAddProgressFoundINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_addprogress_as_found(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `AddProgressProgress` event
func (_self *AddProgress) AsProgress() AddProgressProgress {
	_pointer := _self.ffiObject.incrementPointer("*AddProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterAddProgressProgressINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_addprogress_as_progress(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the type of event
func (_self *AddProgress) Type() AddProgressType {
	_pointer := _self.ffiObject.incrementPointer("*AddProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterAddProgressTypeINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_addprogress_type(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *AddProgress) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterAddProgress struct{}

var FfiConverterAddProgressINSTANCE = FfiConverterAddProgress{}

func (c FfiConverterAddProgress) Lift(pointer unsafe.Pointer) *AddProgress {
	result := &AddProgress{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_addprogress(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_addprogress(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*AddProgress).Destroy)
	return result
}

func (c FfiConverterAddProgress) Read(reader io.Reader) *AddProgress {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterAddProgress) Lower(value *AddProgress) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*AddProgress")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterAddProgress) Write(writer io.Writer, value *AddProgress) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerAddProgress struct{}

func (_ FfiDestroyerAddProgress) Destroy(value *AddProgress) {
	value.Destroy()
}

// Author key to insert entries in a document
//
// Internally, an author is a `SigningKey` which is used to sign entries.
type AuthorInterface interface {
	// Get the [`AuthorId`] of this Author
	Id() *AuthorId
}

// Author key to insert entries in a document
//
// Internally, an author is a `SigningKey` which is used to sign entries.
type Author struct {
	ffiObject FfiObject
}

// Get an [`Author`] from a String
func AuthorFromString(str string) (*Author, *IrohError) {
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_author_from_string(FfiConverterStringINSTANCE.Lower(str), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Author
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterAuthorINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Get the [`AuthorId`] of this Author
func (_self *Author) Id() *AuthorId {
	_pointer := _self.ffiObject.incrementPointer("*Author")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterAuthorIdINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_author_id(
			_pointer, _uniffiStatus)
	}))
}

func (_self *Author) String() string {
	_pointer := _self.ffiObject.incrementPointer("*Author")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_author_uniffi_trait_display(
				_pointer, _uniffiStatus),
		}
	}))
}

func (object *Author) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterAuthor struct{}

var FfiConverterAuthorINSTANCE = FfiConverterAuthor{}

func (c FfiConverterAuthor) Lift(pointer unsafe.Pointer) *Author {
	result := &Author{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_author(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_author(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Author).Destroy)
	return result
}

func (c FfiConverterAuthor) Read(reader io.Reader) *Author {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterAuthor) Lower(value *Author) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Author")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterAuthor) Write(writer io.Writer, value *Author) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerAuthor struct{}

func (_ FfiDestroyerAuthor) Destroy(value *Author) {
	value.Destroy()
}

// Identifier for an [`Author`]
type AuthorIdInterface interface {
	// Returns true when both AuthorId's have the same value
	Equal(other *AuthorId) bool
}

// Identifier for an [`Author`]
type AuthorId struct {
	ffiObject FfiObject
}

// Get an [`AuthorId`] from a String.
func AuthorIdFromString(str string) (*AuthorId, *IrohError) {
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_authorid_from_string(FfiConverterStringINSTANCE.Lower(str), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *AuthorId
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterAuthorIdINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Returns true when both AuthorId's have the same value
func (_self *AuthorId) Equal(other *AuthorId) bool {
	_pointer := _self.ffiObject.incrementPointer("*AuthorId")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_iroh_ffi_fn_method_authorid_equal(
			_pointer, FfiConverterAuthorIdINSTANCE.Lower(other), _uniffiStatus)
	}))
}

func (_self *AuthorId) String() string {
	_pointer := _self.ffiObject.incrementPointer("*AuthorId")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_authorid_uniffi_trait_display(
				_pointer, _uniffiStatus),
		}
	}))
}

func (object *AuthorId) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterAuthorId struct{}

var FfiConverterAuthorIdINSTANCE = FfiConverterAuthorId{}

func (c FfiConverterAuthorId) Lift(pointer unsafe.Pointer) *AuthorId {
	result := &AuthorId{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_authorid(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_authorid(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*AuthorId).Destroy)
	return result
}

func (c FfiConverterAuthorId) Read(reader io.Reader) *AuthorId {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterAuthorId) Lower(value *AuthorId) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*AuthorId")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterAuthorId) Write(writer io.Writer, value *AuthorId) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerAuthorId struct{}

func (_ FfiDestroyerAuthorId) Destroy(value *AuthorId) {
	value.Destroy()
}

// Iroh authors client.
type AuthorsInterface interface {
	// Create a new document author.
	//
	// You likely want to save the returned [`AuthorId`] somewhere so that you can use this author
	// again.
	//
	// If you need only a single author, use [`Self::default`].
	Create() (*AuthorId, *IrohError)
	// Returns the default document author of this node.
	//
	// On persistent nodes, the author is created on first start and its public key is saved
	// in the data directory.
	//
	// The default author can be set with [`Self::set_default`].
	Default() (*AuthorId, *IrohError)
	// Deletes the given author by id.
	//
	// Warning: This permanently removes this author.
	Delete(author *AuthorId) *IrohError
	// Export the given author.
	//
	// Warning: This contains sensitive data.
	Export(author *AuthorId) (*Author, *IrohError)
	// Import the given author.
	//
	// Warning: This contains sensitive data.
	Import(author *Author) (*AuthorId, *IrohError)
	// Import the given author.
	//
	// Warning: This contains sensitive data.
	// `import` is reserved in python.
	ImportAuthor(author *Author) (*AuthorId, *IrohError)
	// List all the AuthorIds that exist on this node.
	List() ([]*AuthorId, *IrohError)
}

// Iroh authors client.
type Authors struct {
	ffiObject FfiObject
}

// Create a new document author.
//
// You likely want to save the returned [`AuthorId`] somewhere so that you can use this author
// again.
//
// If you need only a single author, use [`Self::default`].
func (_self *Authors) Create() (*AuthorId, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Authors")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *AuthorId {
			return FfiConverterAuthorIdINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_authors_create(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Returns the default document author of this node.
//
// On persistent nodes, the author is created on first start and its public key is saved
// in the data directory.
//
// The default author can be set with [`Self::set_default`].
func (_self *Authors) Default() (*AuthorId, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Authors")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *AuthorId {
			return FfiConverterAuthorIdINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_authors_default(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Deletes the given author by id.
//
// Warning: This permanently removes this author.
func (_self *Authors) Delete(author *AuthorId) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Authors")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_authors_delete(
			_pointer, FfiConverterAuthorIdINSTANCE.Lower(author)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Export the given author.
//
// Warning: This contains sensitive data.
func (_self *Authors) Export(author *AuthorId) (*Author, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Authors")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *Author {
			return FfiConverterAuthorINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_authors_export(
			_pointer, FfiConverterAuthorIdINSTANCE.Lower(author)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Import the given author.
//
// Warning: This contains sensitive data.
func (_self *Authors) Import(author *Author) (*AuthorId, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Authors")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *AuthorId {
			return FfiConverterAuthorIdINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_authors_import(
			_pointer, FfiConverterAuthorINSTANCE.Lower(author)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Import the given author.
//
// Warning: This contains sensitive data.
// `import` is reserved in python.
func (_self *Authors) ImportAuthor(author *Author) (*AuthorId, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Authors")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *AuthorId {
			return FfiConverterAuthorIdINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_authors_import_author(
			_pointer, FfiConverterAuthorINSTANCE.Lower(author)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// List all the AuthorIds that exist on this node.
func (_self *Authors) List() ([]*AuthorId, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Authors")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []*AuthorId {
			return FfiConverterSequenceAuthorIdINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_authors_list(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}
func (object *Authors) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterAuthors struct{}

var FfiConverterAuthorsINSTANCE = FfiConverterAuthors{}

func (c FfiConverterAuthors) Lift(pointer unsafe.Pointer) *Authors {
	result := &Authors{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_authors(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_authors(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Authors).Destroy)
	return result
}

func (c FfiConverterAuthors) Read(reader io.Reader) *Authors {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterAuthors) Lower(value *Authors) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Authors")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterAuthors) Write(writer io.Writer, value *Authors) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerAuthors struct{}

func (_ FfiDestroyerAuthors) Destroy(value *Authors) {
	value.Destroy()
}

type BiStreamInterface interface {
	Recv() *RecvStream
	Send() *SendStream
}
type BiStream struct {
	ffiObject FfiObject
}

func (_self *BiStream) Recv() *RecvStream {
	_pointer := _self.ffiObject.incrementPointer("*BiStream")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterRecvStreamINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_bistream_recv(
			_pointer, _uniffiStatus)
	}))
}

func (_self *BiStream) Send() *SendStream {
	_pointer := _self.ffiObject.incrementPointer("*BiStream")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSendStreamINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_bistream_send(
			_pointer, _uniffiStatus)
	}))
}
func (object *BiStream) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterBiStream struct{}

var FfiConverterBiStreamINSTANCE = FfiConverterBiStream{}

func (c FfiConverterBiStream) Lift(pointer unsafe.Pointer) *BiStream {
	result := &BiStream{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_bistream(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_bistream(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*BiStream).Destroy)
	return result
}

func (c FfiConverterBiStream) Read(reader io.Reader) *BiStream {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterBiStream) Lower(value *BiStream) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*BiStream")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterBiStream) Write(writer io.Writer, value *BiStream) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerBiStream struct{}

func (_ FfiDestroyerBiStream) Destroy(value *BiStream) {
	value.Destroy()
}

// Options to download  data specified by the hash.
type BlobDownloadOptionsInterface interface {
}

// Options to download  data specified by the hash.
type BlobDownloadOptions struct {
	ffiObject FfiObject
}

// Create a BlobDownloadRequest
func NewBlobDownloadOptions(format BlobFormat, nodes []*NodeAddr, tag *SetTagOption) (*BlobDownloadOptions, *IrohError) {
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_blobdownloadoptions_new(FfiConverterBlobFormatINSTANCE.Lower(format), FfiConverterSequenceNodeAddrINSTANCE.Lower(nodes), FfiConverterSetTagOptionINSTANCE.Lower(tag), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *BlobDownloadOptions
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBlobDownloadOptionsINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

func (object *BlobDownloadOptions) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterBlobDownloadOptions struct{}

var FfiConverterBlobDownloadOptionsINSTANCE = FfiConverterBlobDownloadOptions{}

func (c FfiConverterBlobDownloadOptions) Lift(pointer unsafe.Pointer) *BlobDownloadOptions {
	result := &BlobDownloadOptions{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_blobdownloadoptions(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_blobdownloadoptions(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*BlobDownloadOptions).Destroy)
	return result
}

func (c FfiConverterBlobDownloadOptions) Read(reader io.Reader) *BlobDownloadOptions {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterBlobDownloadOptions) Lower(value *BlobDownloadOptions) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*BlobDownloadOptions")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterBlobDownloadOptions) Write(writer io.Writer, value *BlobDownloadOptions) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerBlobDownloadOptions struct{}

func (_ FfiDestroyerBlobDownloadOptions) Destroy(value *BlobDownloadOptions) {
	value.Destroy()
}

// Events emitted by the provider informing about the current status.
type BlobProvideEventInterface interface {
	// Return the `ClientConnected` event
	AsClientConnected() ClientConnected
	// Return the `GetRequestReceived` event
	AsGetRequestReceived() GetRequestReceived
	// Return the `TaggedBlobAdded` event
	AsTaggedBlobAdded() TaggedBlobAdded
	// Return the `TransferAborted` event
	AsTransferAborted() TransferAborted
	// Return the `TransferBlobCompleted` event
	AsTransferBlobCompleted() TransferBlobCompleted
	// Return the `TransferCompleted` event
	AsTransferCompleted() TransferCompleted
	// Return the `TransferHashSeqStarted` event
	AsTransferHashSeqStarted() TransferHashSeqStarted
	// Return the `TransferProgress` event
	AsTransferProgress() TransferProgress
	// Get the type of event
	Type() BlobProvideEventType
}

// Events emitted by the provider informing about the current status.
type BlobProvideEvent struct {
	ffiObject FfiObject
}

// Return the `ClientConnected` event
func (_self *BlobProvideEvent) AsClientConnected() ClientConnected {
	_pointer := _self.ffiObject.incrementPointer("*BlobProvideEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterClientConnectedINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_blobprovideevent_as_client_connected(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `GetRequestReceived` event
func (_self *BlobProvideEvent) AsGetRequestReceived() GetRequestReceived {
	_pointer := _self.ffiObject.incrementPointer("*BlobProvideEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterGetRequestReceivedINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_blobprovideevent_as_get_request_received(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `TaggedBlobAdded` event
func (_self *BlobProvideEvent) AsTaggedBlobAdded() TaggedBlobAdded {
	_pointer := _self.ffiObject.incrementPointer("*BlobProvideEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTaggedBlobAddedINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_blobprovideevent_as_tagged_blob_added(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `TransferAborted` event
func (_self *BlobProvideEvent) AsTransferAborted() TransferAborted {
	_pointer := _self.ffiObject.incrementPointer("*BlobProvideEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTransferAbortedINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_blobprovideevent_as_transfer_aborted(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `TransferBlobCompleted` event
func (_self *BlobProvideEvent) AsTransferBlobCompleted() TransferBlobCompleted {
	_pointer := _self.ffiObject.incrementPointer("*BlobProvideEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTransferBlobCompletedINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_blobprovideevent_as_transfer_blob_completed(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `TransferCompleted` event
func (_self *BlobProvideEvent) AsTransferCompleted() TransferCompleted {
	_pointer := _self.ffiObject.incrementPointer("*BlobProvideEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTransferCompletedINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_blobprovideevent_as_transfer_completed(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `TransferHashSeqStarted` event
func (_self *BlobProvideEvent) AsTransferHashSeqStarted() TransferHashSeqStarted {
	_pointer := _self.ffiObject.incrementPointer("*BlobProvideEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTransferHashSeqStartedINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_blobprovideevent_as_transfer_hash_seq_started(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `TransferProgress` event
func (_self *BlobProvideEvent) AsTransferProgress() TransferProgress {
	_pointer := _self.ffiObject.incrementPointer("*BlobProvideEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTransferProgressINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_blobprovideevent_as_transfer_progress(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the type of event
func (_self *BlobProvideEvent) Type() BlobProvideEventType {
	_pointer := _self.ffiObject.incrementPointer("*BlobProvideEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBlobProvideEventTypeINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_blobprovideevent_type(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *BlobProvideEvent) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterBlobProvideEvent struct{}

var FfiConverterBlobProvideEventINSTANCE = FfiConverterBlobProvideEvent{}

func (c FfiConverterBlobProvideEvent) Lift(pointer unsafe.Pointer) *BlobProvideEvent {
	result := &BlobProvideEvent{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_blobprovideevent(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_blobprovideevent(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*BlobProvideEvent).Destroy)
	return result
}

func (c FfiConverterBlobProvideEvent) Read(reader io.Reader) *BlobProvideEvent {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterBlobProvideEvent) Lower(value *BlobProvideEvent) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*BlobProvideEvent")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterBlobProvideEvent) Write(writer io.Writer, value *BlobProvideEvent) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerBlobProvideEvent struct{}

func (_ FfiDestroyerBlobProvideEvent) Destroy(value *BlobProvideEvent) {
	value.Destroy()
}

// The `progress` method will be called for each `BlobProvideEvent` event that is
// emitted from the iroh node while the callback is registered. Use the `BlobProvideEvent.type()`
// method to check the `BlobProvideEventType`
type BlobProvideEventCallback interface {
	BlobEvent(event *BlobProvideEvent) *CallbackError
}

// The `progress` method will be called for each `BlobProvideEvent` event that is
// emitted from the iroh node while the callback is registered. Use the `BlobProvideEvent.type()`
// method to check the `BlobProvideEventType`
type BlobProvideEventCallbackImpl struct {
	ffiObject FfiObject
}

func (_self *BlobProvideEventCallbackImpl) BlobEvent(event *BlobProvideEvent) *CallbackError {
	_pointer := _self.ffiObject.incrementPointer("BlobProvideEventCallback")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[CallbackError](
		FfiConverterCallbackErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_blobprovideeventcallback_blob_event(
			_pointer, FfiConverterBlobProvideEventINSTANCE.Lower(event)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}
func (object *BlobProvideEventCallbackImpl) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterBlobProvideEventCallback struct {
	handleMap *concurrentHandleMap[BlobProvideEventCallback]
}

var FfiConverterBlobProvideEventCallbackINSTANCE = FfiConverterBlobProvideEventCallback{
	handleMap: newConcurrentHandleMap[BlobProvideEventCallback](),
}

func (c FfiConverterBlobProvideEventCallback) Lift(pointer unsafe.Pointer) BlobProvideEventCallback {
	result := &BlobProvideEventCallbackImpl{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_blobprovideeventcallback(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_blobprovideeventcallback(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*BlobProvideEventCallbackImpl).Destroy)
	return result
}

func (c FfiConverterBlobProvideEventCallback) Read(reader io.Reader) BlobProvideEventCallback {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterBlobProvideEventCallback) Lower(value BlobProvideEventCallback) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := unsafe.Pointer(uintptr(c.handleMap.insert(value)))
	return pointer

}

func (c FfiConverterBlobProvideEventCallback) Write(writer io.Writer, value BlobProvideEventCallback) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerBlobProvideEventCallback struct{}

func (_ FfiDestroyerBlobProvideEventCallback) Destroy(value BlobProvideEventCallback) {
	if val, ok := value.(*BlobProvideEventCallbackImpl); ok {
		val.Destroy()
	} else {
		panic("Expected *BlobProvideEventCallbackImpl")
	}
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceBlobProvideEventCallbackMethod0
func iroh_ffi_cgo_dispatchCallbackInterfaceBlobProvideEventCallbackMethod0(uniffiHandle C.uint64_t, event unsafe.Pointer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterBlobProvideEventCallbackINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.iroh_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.BlobEvent(
				FfiConverterBlobProvideEventINSTANCE.Lift(event),
			)

		if err != nil {
			// The only way to bypass an unexpected error is to bypass pointer to an empty
			// instance of the error
			if err.err == nil {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
				return
			}

			*callStatus = C.RustCallStatus{
				code:     C.int8_t(uniffiCallbackResultError),
				errorBuf: FfiConverterCallbackErrorINSTANCE.Lower(err),
			}
			return
		}

	}()
}

var UniffiVTableCallbackInterfaceBlobProvideEventCallbackINSTANCE = C.UniffiVTableCallbackInterfaceBlobProvideEventCallback{
	blobEvent: (C.UniffiCallbackInterfaceBlobProvideEventCallbackMethod0)(C.iroh_ffi_cgo_dispatchCallbackInterfaceBlobProvideEventCallbackMethod0),

	uniffiFree: (C.UniffiCallbackInterfaceFree)(C.iroh_ffi_cgo_dispatchCallbackInterfaceBlobProvideEventCallbackFree),
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceBlobProvideEventCallbackFree
func iroh_ffi_cgo_dispatchCallbackInterfaceBlobProvideEventCallbackFree(handle C.uint64_t) {
	FfiConverterBlobProvideEventCallbackINSTANCE.handleMap.remove(uint64(handle))
}

func (c FfiConverterBlobProvideEventCallback) register() {
	C.uniffi_iroh_ffi_fn_init_callback_vtable_blobprovideeventcallback(&UniffiVTableCallbackInterfaceBlobProvideEventCallbackINSTANCE)
}

// Status information about a blob.
type BlobStatusInterface interface {
}

// Status information about a blob.
type BlobStatus struct {
	ffiObject FfiObject
}

func (object *BlobStatus) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterBlobStatus struct{}

var FfiConverterBlobStatusINSTANCE = FfiConverterBlobStatus{}

func (c FfiConverterBlobStatus) Lift(pointer unsafe.Pointer) *BlobStatus {
	result := &BlobStatus{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_blobstatus(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_blobstatus(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*BlobStatus).Destroy)
	return result
}

func (c FfiConverterBlobStatus) Read(reader io.Reader) *BlobStatus {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterBlobStatus) Lower(value *BlobStatus) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*BlobStatus")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterBlobStatus) Write(writer io.Writer, value *BlobStatus) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerBlobStatus struct{}

func (_ FfiDestroyerBlobStatus) Destroy(value *BlobStatus) {
	value.Destroy()
}

// A token containing everything to get a file from the provider.
//
// It is a single item which can be easily serialized and deserialized.
type BlobTicketInterface interface {
	// Convert this ticket into input parameters for a call to blobs_download
	AsDownloadOptions() *BlobDownloadOptions
	// The [`BlobFormat`] for this ticket.
	Format() BlobFormat
	// The hash of the item this ticket can retrieve.
	Hash() *Hash
	// The [`NodeAddr`] of the provider for this ticket.
	NodeAddr() *NodeAddr
	// True if the ticket is for a collection and should retrieve all blobs in it.
	Recursive() bool
}

// A token containing everything to get a file from the provider.
//
// It is a single item which can be easily serialized and deserialized.
type BlobTicket struct {
	ffiObject FfiObject
}

func NewBlobTicket(str string) (*BlobTicket, *IrohError) {
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_blobticket_new(FfiConverterStringINSTANCE.Lower(str), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *BlobTicket
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBlobTicketINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Convert this ticket into input parameters for a call to blobs_download
func (_self *BlobTicket) AsDownloadOptions() *BlobDownloadOptions {
	_pointer := _self.ffiObject.incrementPointer("*BlobTicket")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBlobDownloadOptionsINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_blobticket_as_download_options(
			_pointer, _uniffiStatus)
	}))
}

// The [`BlobFormat`] for this ticket.
func (_self *BlobTicket) Format() BlobFormat {
	_pointer := _self.ffiObject.incrementPointer("*BlobTicket")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBlobFormatINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_blobticket_format(
				_pointer, _uniffiStatus),
		}
	}))
}

// The hash of the item this ticket can retrieve.
func (_self *BlobTicket) Hash() *Hash {
	_pointer := _self.ffiObject.incrementPointer("*BlobTicket")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterHashINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_blobticket_hash(
			_pointer, _uniffiStatus)
	}))
}

// The [`NodeAddr`] of the provider for this ticket.
func (_self *BlobTicket) NodeAddr() *NodeAddr {
	_pointer := _self.ffiObject.incrementPointer("*BlobTicket")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterNodeAddrINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_blobticket_node_addr(
			_pointer, _uniffiStatus)
	}))
}

// True if the ticket is for a collection and should retrieve all blobs in it.
func (_self *BlobTicket) Recursive() bool {
	_pointer := _self.ffiObject.incrementPointer("*BlobTicket")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_iroh_ffi_fn_method_blobticket_recursive(
			_pointer, _uniffiStatus)
	}))
}

func (_self *BlobTicket) String() string {
	_pointer := _self.ffiObject.incrementPointer("*BlobTicket")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_blobticket_uniffi_trait_display(
				_pointer, _uniffiStatus),
		}
	}))
}

func (object *BlobTicket) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterBlobTicket struct{}

var FfiConverterBlobTicketINSTANCE = FfiConverterBlobTicket{}

func (c FfiConverterBlobTicket) Lift(pointer unsafe.Pointer) *BlobTicket {
	result := &BlobTicket{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_blobticket(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_blobticket(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*BlobTicket).Destroy)
	return result
}

func (c FfiConverterBlobTicket) Read(reader io.Reader) *BlobTicket {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterBlobTicket) Lower(value *BlobTicket) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*BlobTicket")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterBlobTicket) Write(writer io.Writer, value *BlobTicket) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerBlobTicket struct{}

func (_ FfiDestroyerBlobTicket) Destroy(value *BlobTicket) {
	value.Destroy()
}

// Iroh blobs client.
type BlobsInterface interface {
	// Write a blob by passing bytes.
	AddBytes(bytes []byte) (BlobAddOutcome, *IrohError)
	// Write a blob by passing bytes, setting an explicit tag name.
	AddBytesNamed(bytes []byte, name string) (BlobAddOutcome, *IrohError)
	// Import a blob from a filesystem path.
	//
	// `path` should be an absolute path valid for the file system on which
	// the node runs.
	// If `in_place` is true, Iroh will assume that the data will not change and will share it in
	// place without copying to the Iroh data directory.
	AddFromPath(path string, inPlace bool, tag *SetTagOption, wrap *WrapOption, cb AddCallback) *IrohError
	// Create a collection from already existing blobs.
	//
	// To automatically clear the tags for the passed in blobs you can set
	// `tags_to_delete` on those tags, and they will be deleted once the collection is created.
	CreateCollection(collection *Collection, tag *SetTagOption, tagsToDelete []string) (HashAndTag, *IrohError)
	// Delete a blob.
	DeleteBlob(hash *Hash) *IrohError
	// Download a blob from another node and add it to the local database.
	Download(hash *Hash, opts *BlobDownloadOptions, cb DownloadCallback) *IrohError
	// Export a blob from the internal blob store to a path on the node's filesystem.
	//
	// `destination` should be a writeable, absolute path on the local node's filesystem.
	//
	// If `format` is set to [`ExportFormat::Collection`], and the `hash` refers to a collection,
	// all children of the collection will be exported. See [`ExportFormat`] for details.
	//
	// The `mode` argument defines if the blob should be copied to the target location or moved out of
	// the internal store into the target location. See [`ExportMode`] for details.
	Export(hash *Hash, destination string, format BlobExportFormat, mode BlobExportMode) *IrohError
	// Read the content of a collection
	GetCollection(hash *Hash) (*Collection, *IrohError)
	// Check if a blob is completely stored on the node.
	//
	// This is just a convenience wrapper around `status` that returns a boolean.
	Has(hash *Hash) (bool, *IrohError)
	// List all complete blobs.
	//
	// Note: this allocates for each `BlobListResponse`, if you have many `BlobListReponse`s this may be a prohibitively large list.
	// Please file an [issue](https://github.com/n0-computer/iroh-ffi/issues/new) if you run into this issue
	List() ([]*Hash, *IrohError)
	// List all collections.
	//
	// Note: this allocates for each `BlobListCollectionsResponse`, if you have many `BlobListCollectionsResponse`s this may be a prohibitively large list.
	// Please file an [issue](https://github.com/n0-computer/iroh-ffi/issues/new) if you run into this issue
	ListCollections() ([]CollectionInfo, *IrohError)
	// List all incomplete (partial) blobs.
	//
	// Note: this allocates for each `BlobListIncompleteResponse`, if you have many `BlobListIncompleteResponse`s this may be a prohibitively large list.
	// Please file an [issue](https://github.com/n0-computer/iroh-ffi/issues/new) if you run into this issue
	ListIncomplete() ([]IncompleteBlobInfo, *IrohError)
	// Read all bytes of single blob at `offset` for length `len`.
	//
	// This allocates a buffer for the full length `len`. Use only if you know that the blob you're
	// reading is small. If not sure, use [`Self::blobs_size`] and check the size with
	// before calling [`Self::blobs_read_at_to_bytes`].
	ReadAtToBytes(hash *Hash, offset uint64, len *ReadAtLen) ([]byte, *IrohError)
	// Read all bytes of single blob.
	//
	// This allocates a buffer for the full blob. Use only if you know that the blob you're
	// reading is small. If not sure, use [`Self::blobs_size`] and check the size with
	// before calling [`Self::blobs_read_to_bytes`].
	ReadToBytes(hash *Hash) ([]byte, *IrohError)
	// Create a ticket for sharing a blob from this node.
	Share(hash *Hash, blobFormat BlobFormat, ticketOptions AddrInfoOptions) (*BlobTicket, *IrohError)
	// Get the size information on a single blob.
	//
	// Method only exists in FFI
	Size(hash *Hash) (uint64, *IrohError)
	// Check the storage status of a blob on this node.
	Status(hash *Hash) (*BlobStatus, *IrohError)
	// Export the blob contents to a file path
	// The `path` field is expected to be the absolute path.
	WriteToPath(hash *Hash, path string) *IrohError
}

// Iroh blobs client.
type Blobs struct {
	ffiObject FfiObject
}

// Write a blob by passing bytes.
func (_self *Blobs) AddBytes(bytes []byte) (BlobAddOutcome, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) BlobAddOutcome {
			return FfiConverterBlobAddOutcomeINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_blobs_add_bytes(
			_pointer, FfiConverterBytesINSTANCE.Lower(bytes)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Write a blob by passing bytes, setting an explicit tag name.
func (_self *Blobs) AddBytesNamed(bytes []byte, name string) (BlobAddOutcome, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) BlobAddOutcome {
			return FfiConverterBlobAddOutcomeINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_blobs_add_bytes_named(
			_pointer, FfiConverterBytesINSTANCE.Lower(bytes), FfiConverterStringINSTANCE.Lower(name)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Import a blob from a filesystem path.
//
// `path` should be an absolute path valid for the file system on which
// the node runs.
// If `in_place` is true, Iroh will assume that the data will not change and will share it in
// place without copying to the Iroh data directory.
func (_self *Blobs) AddFromPath(path string, inPlace bool, tag *SetTagOption, wrap *WrapOption, cb AddCallback) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_blobs_add_from_path(
			_pointer, FfiConverterStringINSTANCE.Lower(path), FfiConverterBoolINSTANCE.Lower(inPlace), FfiConverterSetTagOptionINSTANCE.Lower(tag), FfiConverterWrapOptionINSTANCE.Lower(wrap), FfiConverterAddCallbackINSTANCE.Lower(cb)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Create a collection from already existing blobs.
//
// To automatically clear the tags for the passed in blobs you can set
// `tags_to_delete` on those tags, and they will be deleted once the collection is created.
func (_self *Blobs) CreateCollection(collection *Collection, tag *SetTagOption, tagsToDelete []string) (HashAndTag, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) HashAndTag {
			return FfiConverterHashAndTagINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_blobs_create_collection(
			_pointer, FfiConverterCollectionINSTANCE.Lower(collection), FfiConverterSetTagOptionINSTANCE.Lower(tag), FfiConverterSequenceStringINSTANCE.Lower(tagsToDelete)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Delete a blob.
func (_self *Blobs) DeleteBlob(hash *Hash) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_blobs_delete_blob(
			_pointer, FfiConverterHashINSTANCE.Lower(hash)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Download a blob from another node and add it to the local database.
func (_self *Blobs) Download(hash *Hash, opts *BlobDownloadOptions, cb DownloadCallback) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_blobs_download(
			_pointer, FfiConverterHashINSTANCE.Lower(hash), FfiConverterBlobDownloadOptionsINSTANCE.Lower(opts), FfiConverterDownloadCallbackINSTANCE.Lower(cb)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Export a blob from the internal blob store to a path on the node's filesystem.
//
// `destination` should be a writeable, absolute path on the local node's filesystem.
//
// If `format` is set to [`ExportFormat::Collection`], and the `hash` refers to a collection,
// all children of the collection will be exported. See [`ExportFormat`] for details.
//
// The `mode` argument defines if the blob should be copied to the target location or moved out of
// the internal store into the target location. See [`ExportMode`] for details.
func (_self *Blobs) Export(hash *Hash, destination string, format BlobExportFormat, mode BlobExportMode) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_blobs_export(
			_pointer, FfiConverterHashINSTANCE.Lower(hash), FfiConverterStringINSTANCE.Lower(destination), FfiConverterBlobExportFormatINSTANCE.Lower(format), FfiConverterBlobExportModeINSTANCE.Lower(mode)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Read the content of a collection
func (_self *Blobs) GetCollection(hash *Hash) (*Collection, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *Collection {
			return FfiConverterCollectionINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_blobs_get_collection(
			_pointer, FfiConverterHashINSTANCE.Lower(hash)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Check if a blob is completely stored on the node.
//
// This is just a convenience wrapper around `status` that returns a boolean.
func (_self *Blobs) Has(hash *Hash) (bool, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) C.int8_t {
			res := C.ffi_iroh_ffi_rust_future_complete_i8(handle, status)
			return res
		},
		// liftFn
		func(ffi C.int8_t) bool {
			return FfiConverterBoolINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_blobs_has(
			_pointer, FfiConverterHashINSTANCE.Lower(hash)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_i8(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_i8(handle)
		},
	)

	return res, err
}

// List all complete blobs.
//
// Note: this allocates for each `BlobListResponse`, if you have many `BlobListReponse`s this may be a prohibitively large list.
// Please file an [issue](https://github.com/n0-computer/iroh-ffi/issues/new) if you run into this issue
func (_self *Blobs) List() ([]*Hash, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []*Hash {
			return FfiConverterSequenceHashINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_blobs_list(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// List all collections.
//
// Note: this allocates for each `BlobListCollectionsResponse`, if you have many `BlobListCollectionsResponse`s this may be a prohibitively large list.
// Please file an [issue](https://github.com/n0-computer/iroh-ffi/issues/new) if you run into this issue
func (_self *Blobs) ListCollections() ([]CollectionInfo, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []CollectionInfo {
			return FfiConverterSequenceCollectionInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_blobs_list_collections(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// List all incomplete (partial) blobs.
//
// Note: this allocates for each `BlobListIncompleteResponse`, if you have many `BlobListIncompleteResponse`s this may be a prohibitively large list.
// Please file an [issue](https://github.com/n0-computer/iroh-ffi/issues/new) if you run into this issue
func (_self *Blobs) ListIncomplete() ([]IncompleteBlobInfo, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []IncompleteBlobInfo {
			return FfiConverterSequenceIncompleteBlobInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_blobs_list_incomplete(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Read all bytes of single blob at `offset` for length `len`.
//
// This allocates a buffer for the full length `len`. Use only if you know that the blob you're
// reading is small. If not sure, use [`Self::blobs_size`] and check the size with
// before calling [`Self::blobs_read_at_to_bytes`].
func (_self *Blobs) ReadAtToBytes(hash *Hash, offset uint64, len *ReadAtLen) ([]byte, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []byte {
			return FfiConverterBytesINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_blobs_read_at_to_bytes(
			_pointer, FfiConverterHashINSTANCE.Lower(hash), FfiConverterUint64INSTANCE.Lower(offset), FfiConverterReadAtLenINSTANCE.Lower(len)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Read all bytes of single blob.
//
// This allocates a buffer for the full blob. Use only if you know that the blob you're
// reading is small. If not sure, use [`Self::blobs_size`] and check the size with
// before calling [`Self::blobs_read_to_bytes`].
func (_self *Blobs) ReadToBytes(hash *Hash) ([]byte, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []byte {
			return FfiConverterBytesINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_blobs_read_to_bytes(
			_pointer, FfiConverterHashINSTANCE.Lower(hash)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Create a ticket for sharing a blob from this node.
func (_self *Blobs) Share(hash *Hash, blobFormat BlobFormat, ticketOptions AddrInfoOptions) (*BlobTicket, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *BlobTicket {
			return FfiConverterBlobTicketINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_blobs_share(
			_pointer, FfiConverterHashINSTANCE.Lower(hash), FfiConverterBlobFormatINSTANCE.Lower(blobFormat), FfiConverterAddrInfoOptionsINSTANCE.Lower(ticketOptions)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Get the size information on a single blob.
//
// Method only exists in FFI
func (_self *Blobs) Size(hash *Hash) (uint64, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) C.uint64_t {
			res := C.ffi_iroh_ffi_rust_future_complete_u64(handle, status)
			return res
		},
		// liftFn
		func(ffi C.uint64_t) uint64 {
			return FfiConverterUint64INSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_blobs_size(
			_pointer, FfiConverterHashINSTANCE.Lower(hash)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_u64(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_u64(handle)
		},
	)

	return res, err
}

// Check the storage status of a blob on this node.
func (_self *Blobs) Status(hash *Hash) (*BlobStatus, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *BlobStatus {
			return FfiConverterBlobStatusINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_blobs_status(
			_pointer, FfiConverterHashINSTANCE.Lower(hash)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Export the blob contents to a file path
// The `path` field is expected to be the absolute path.
func (_self *Blobs) WriteToPath(hash *Hash, path string) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Blobs")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_blobs_write_to_path(
			_pointer, FfiConverterHashINSTANCE.Lower(hash), FfiConverterStringINSTANCE.Lower(path)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}
func (object *Blobs) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterBlobs struct{}

var FfiConverterBlobsINSTANCE = FfiConverterBlobs{}

func (c FfiConverterBlobs) Lift(pointer unsafe.Pointer) *Blobs {
	result := &Blobs{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_blobs(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_blobs(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Blobs).Destroy)
	return result
}

func (c FfiConverterBlobs) Read(reader io.Reader) *Blobs {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterBlobs) Lower(value *Blobs) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Blobs")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterBlobs) Write(writer io.Writer, value *Blobs) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerBlobs struct{}

func (_ FfiDestroyerBlobs) Destroy(value *Blobs) {
	value.Destroy()
}

// A collection of blobs
type CollectionInterface interface {
	// Get the blobs associated with this collection
	Blobs() ([]LinkAndName, *IrohError)
	// Check if the collection is empty
	IsEmpty() (bool, *IrohError)
	// Returns the number of blobs in this collection
	Len() (uint64, *IrohError)
	// Get the links to the blobs in this collection
	Links() ([]*Hash, *IrohError)
	// Get the names of the blobs in this collection
	Names() ([]string, *IrohError)
	// Add the given blob to the collection
	Push(name string, hash *Hash) *IrohError
}

// A collection of blobs
type Collection struct {
	ffiObject FfiObject
}

// Create a new empty collection
func NewCollection() *Collection {
	return FfiConverterCollectionINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_collection_new(_uniffiStatus)
	}))
}

// Get the blobs associated with this collection
func (_self *Collection) Blobs() ([]LinkAndName, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Collection")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_collection_blobs(
				_pointer, _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue []LinkAndName
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterSequenceLinkAndNameINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Check if the collection is empty
func (_self *Collection) IsEmpty() (bool, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Collection")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_iroh_ffi_fn_method_collection_is_empty(
			_pointer, _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue bool
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBoolINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Returns the number of blobs in this collection
func (_self *Collection) Len() (uint64, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Collection")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_iroh_ffi_fn_method_collection_len(
			_pointer, _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue uint64
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterUint64INSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Get the links to the blobs in this collection
func (_self *Collection) Links() ([]*Hash, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Collection")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_collection_links(
				_pointer, _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue []*Hash
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterSequenceHashINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Get the names of the blobs in this collection
func (_self *Collection) Names() ([]string, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Collection")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_collection_names(
				_pointer, _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue []string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterSequenceStringINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Add the given blob to the collection
func (_self *Collection) Push(name string, hash *Hash) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Collection")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_iroh_ffi_fn_method_collection_push(
			_pointer, FfiConverterStringINSTANCE.Lower(name), FfiConverterHashINSTANCE.Lower(hash), _uniffiStatus)
		return false
	})
	return _uniffiErr
}
func (object *Collection) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterCollection struct{}

var FfiConverterCollectionINSTANCE = FfiConverterCollection{}

func (c FfiConverterCollection) Lift(pointer unsafe.Pointer) *Collection {
	result := &Collection{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_collection(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_collection(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Collection).Destroy)
	return result
}

func (c FfiConverterCollection) Read(reader io.Reader) *Collection {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterCollection) Lower(value *Collection) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Collection")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterCollection) Write(writer io.Writer, value *Collection) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerCollection struct{}

func (_ FfiDestroyerCollection) Destroy(value *Collection) {
	value.Destroy()
}

type ConnectingInterface interface {
	Alpn() ([]byte, *IrohError)
	Connect() (*Connection, *IrohError)
}
type Connecting struct {
	ffiObject FfiObject
}

func (_self *Connecting) Alpn() ([]byte, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Connecting")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []byte {
			return FfiConverterBytesINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_connecting_alpn(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

func (_self *Connecting) Connect() (*Connection, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Connecting")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *Connection {
			return FfiConverterConnectionINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_connecting_connect(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}
func (object *Connecting) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterConnecting struct{}

var FfiConverterConnectingINSTANCE = FfiConverterConnecting{}

func (c FfiConverterConnecting) Lift(pointer unsafe.Pointer) *Connecting {
	result := &Connecting{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_connecting(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_connecting(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Connecting).Destroy)
	return result
}

func (c FfiConverterConnecting) Read(reader io.Reader) *Connecting {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterConnecting) Lower(value *Connecting) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Connecting")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterConnecting) Write(writer io.Writer, value *Connecting) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerConnecting struct{}

func (_ FfiDestroyerConnecting) Destroy(value *Connecting) {
	value.Destroy()
}

type ConnectionInterface interface {
	AcceptBi() (*BiStream, *IrohError)
	AcceptUni() (*RecvStream, *IrohError)
	Alpn() *[]byte
	Close(errorCode uint64, reason []byte) *IrohError
	CloseReason() *string
	Closed() string
	DatagramSendBufferSpace() uint64
	MaxDatagramSize() *uint64
	OpenBi() (*BiStream, *IrohError)
	OpenUni() (*SendStream, *IrohError)
	ReadDatagram() ([]byte, *IrohError)
	RemoteNodeId() (string, *IrohError)
	Rtt() uint64
	SendDatagram(data []byte) *IrohError
	SetMaxConcurrentBiiStream(count uint64) *IrohError
	SetMaxConcurrentUniStream(count uint64) *IrohError
	SetReceiveWindow(count uint64) *IrohError
	StableId() uint64
}
type Connection struct {
	ffiObject FfiObject
}

func (_self *Connection) AcceptBi() (*BiStream, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *BiStream {
			return FfiConverterBiStreamINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_connection_accept_bi(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

func (_self *Connection) AcceptUni() (*RecvStream, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *RecvStream {
			return FfiConverterRecvStreamINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_connection_accept_uni(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

func (_self *Connection) Alpn() *[]byte {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalBytesINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_connection_alpn(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Connection) Close(errorCode uint64, reason []byte) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_iroh_ffi_fn_method_connection_close(
			_pointer, FfiConverterUint64INSTANCE.Lower(errorCode), FfiConverterBytesINSTANCE.Lower(reason), _uniffiStatus)
		return false
	})
	return _uniffiErr
}

func (_self *Connection) CloseReason() *string {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_connection_close_reason(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Connection) Closed() string {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	res, _ := uniffiRustCallAsync[struct{}](
		nil,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) string {
			return FfiConverterStringINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_connection_closed(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res
}

func (_self *Connection) DatagramSendBufferSpace() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_iroh_ffi_fn_method_connection_datagram_send_buffer_space(
			_pointer, _uniffiStatus)
	}))
}

func (_self *Connection) MaxDatagramSize() *uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_connection_max_datagram_size(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Connection) OpenBi() (*BiStream, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *BiStream {
			return FfiConverterBiStreamINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_connection_open_bi(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

func (_self *Connection) OpenUni() (*SendStream, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *SendStream {
			return FfiConverterSendStreamINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_connection_open_uni(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

func (_self *Connection) ReadDatagram() ([]byte, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []byte {
			return FfiConverterBytesINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_connection_read_datagram(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

func (_self *Connection) RemoteNodeId() (string, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_connection_remote_node_id(
				_pointer, _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

func (_self *Connection) Rtt() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_iroh_ffi_fn_method_connection_rtt(
			_pointer, _uniffiStatus)
	}))
}

func (_self *Connection) SendDatagram(data []byte) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_iroh_ffi_fn_method_connection_send_datagram(
			_pointer, FfiConverterBytesINSTANCE.Lower(data), _uniffiStatus)
		return false
	})
	return _uniffiErr
}

func (_self *Connection) SetMaxConcurrentBiiStream(count uint64) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_iroh_ffi_fn_method_connection_set_max_concurrent_bii_stream(
			_pointer, FfiConverterUint64INSTANCE.Lower(count), _uniffiStatus)
		return false
	})
	return _uniffiErr
}

func (_self *Connection) SetMaxConcurrentUniStream(count uint64) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_iroh_ffi_fn_method_connection_set_max_concurrent_uni_stream(
			_pointer, FfiConverterUint64INSTANCE.Lower(count), _uniffiStatus)
		return false
	})
	return _uniffiErr
}

func (_self *Connection) SetReceiveWindow(count uint64) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_iroh_ffi_fn_method_connection_set_receive_window(
			_pointer, FfiConverterUint64INSTANCE.Lower(count), _uniffiStatus)
		return false
	})
	return _uniffiErr
}

func (_self *Connection) StableId() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Connection")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_iroh_ffi_fn_method_connection_stable_id(
			_pointer, _uniffiStatus)
	}))
}
func (object *Connection) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterConnection struct{}

var FfiConverterConnectionINSTANCE = FfiConverterConnection{}

func (c FfiConverterConnection) Lift(pointer unsafe.Pointer) *Connection {
	result := &Connection{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_connection(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_connection(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Connection).Destroy)
	return result
}

func (c FfiConverterConnection) Read(reader io.Reader) *Connection {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterConnection) Lower(value *Connection) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Connection")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterConnection) Write(writer io.Writer, value *Connection) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerConnection struct{}

func (_ FfiDestroyerConnection) Destroy(value *Connection) {
	value.Destroy()
}

// The type of connection we have to the node
type ConnectionTypeInterface interface {
	// Return the socket address if this is a direct connection
	AsDirect() string
	// Return the socket address and DERP url if this is a mixed connection
	AsMixed() ConnectionTypeMixed
	// Return the derp url if this is a relay connection
	AsRelay() string
	// Whether connection is direct, relay, mixed, or none
	Type() ConnType
}

// The type of connection we have to the node
type ConnectionType struct {
	ffiObject FfiObject
}

// Return the socket address if this is a direct connection
func (_self *ConnectionType) AsDirect() string {
	_pointer := _self.ffiObject.incrementPointer("*ConnectionType")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_connectiontype_as_direct(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the socket address and DERP url if this is a mixed connection
func (_self *ConnectionType) AsMixed() ConnectionTypeMixed {
	_pointer := _self.ffiObject.incrementPointer("*ConnectionType")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterConnectionTypeMixedINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_connectiontype_as_mixed(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the derp url if this is a relay connection
func (_self *ConnectionType) AsRelay() string {
	_pointer := _self.ffiObject.incrementPointer("*ConnectionType")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_connectiontype_as_relay(
				_pointer, _uniffiStatus),
		}
	}))
}

// Whether connection is direct, relay, mixed, or none
func (_self *ConnectionType) Type() ConnType {
	_pointer := _self.ffiObject.incrementPointer("*ConnectionType")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterConnTypeINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_connectiontype_type(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *ConnectionType) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterConnectionType struct{}

var FfiConverterConnectionTypeINSTANCE = FfiConverterConnectionType{}

func (c FfiConverterConnectionType) Lift(pointer unsafe.Pointer) *ConnectionType {
	result := &ConnectionType{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_connectiontype(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_connectiontype(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*ConnectionType).Destroy)
	return result
}

func (c FfiConverterConnectionType) Read(reader io.Reader) *ConnectionType {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterConnectionType) Lower(value *ConnectionType) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*ConnectionType")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterConnectionType) Write(writer io.Writer, value *ConnectionType) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerConnectionType struct{}

func (_ FfiDestroyerConnectionType) Destroy(value *ConnectionType) {
	value.Destroy()
}

// Information about a direct address.
type DirectAddrInfoInterface interface {
	// Get the reported address
	Addr() string
	// Get the last control message received by this node
	LastControl() *LatencyAndControlMsg
	// Get how long ago the last payload message was received for this node
	LastPayload() *time.Duration
	// Get the reported latency, if it exists
	Latency() *time.Duration
}

// Information about a direct address.
type DirectAddrInfo struct {
	ffiObject FfiObject
}

// Get the reported address
func (_self *DirectAddrInfo) Addr() string {
	_pointer := _self.ffiObject.incrementPointer("*DirectAddrInfo")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_directaddrinfo_addr(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the last control message received by this node
func (_self *DirectAddrInfo) LastControl() *LatencyAndControlMsg {
	_pointer := _self.ffiObject.incrementPointer("*DirectAddrInfo")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalLatencyAndControlMsgINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_directaddrinfo_last_control(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get how long ago the last payload message was received for this node
func (_self *DirectAddrInfo) LastPayload() *time.Duration {
	_pointer := _self.ffiObject.incrementPointer("*DirectAddrInfo")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalDurationINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_directaddrinfo_last_payload(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the reported latency, if it exists
func (_self *DirectAddrInfo) Latency() *time.Duration {
	_pointer := _self.ffiObject.incrementPointer("*DirectAddrInfo")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalDurationINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_directaddrinfo_latency(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *DirectAddrInfo) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterDirectAddrInfo struct{}

var FfiConverterDirectAddrInfoINSTANCE = FfiConverterDirectAddrInfo{}

func (c FfiConverterDirectAddrInfo) Lift(pointer unsafe.Pointer) *DirectAddrInfo {
	result := &DirectAddrInfo{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_directaddrinfo(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_directaddrinfo(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*DirectAddrInfo).Destroy)
	return result
}

func (c FfiConverterDirectAddrInfo) Read(reader io.Reader) *DirectAddrInfo {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterDirectAddrInfo) Lower(value *DirectAddrInfo) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*DirectAddrInfo")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterDirectAddrInfo) Write(writer io.Writer, value *DirectAddrInfo) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerDirectAddrInfo struct{}

func (_ FfiDestroyerDirectAddrInfo) Destroy(value *DirectAddrInfo) {
	value.Destroy()
}

// A representation of a mutable, synchronizable key-value store.
type DocInterface interface {
	// Close the document.
	CloseMe() *IrohError
	// Delete entries that match the given `author` and key `prefix`.
	//
	// This inserts an empty entry with the key set to `prefix`, effectively clearing all other
	// entries whose key starts with or is equal to the given `prefix`.
	//
	// Returns the number of entries deleted.
	Delete(authorId *AuthorId, prefix []byte) (uint64, *IrohError)
	// Export an entry as a file to a given absolute path
	ExportFile(entry *Entry, path string, cb *DocExportFileCallback) *IrohError
	// Get the download policy for this document
	GetDownloadPolicy() (*DownloadPolicy, *IrohError)
	// Get an entry for a key and author.
	GetExact(author *AuthorId, key []byte, includeEmpty bool) (**Entry, *IrohError)
	// Get entries.
	//
	// Note: this allocates for each `Entry`, if you have many `Entry`s this may be a prohibitively large list.
	// Please file an [issue](https://github.com/n0-computer/iroh-ffi/issues/new) if you run into this issue
	GetMany(query *Query) ([]*Entry, *IrohError)
	// Get the latest entry for a key and author.
	GetOne(query *Query) (**Entry, *IrohError)
	// Get sync peers for this document
	GetSyncPeers() (*[][]byte, *IrohError)
	// Get the document id of this doc.
	Id() string
	// Add an entry from an absolute file path
	ImportFile(author *AuthorId, key []byte, path string, inPlace bool, cb *DocImportFileCallback) *IrohError
	// Stop the live sync for this document.
	Leave() *IrohError
	// Set the content of a key to a byte array.
	SetBytes(authorId *AuthorId, key []byte, value []byte) (*Hash, *IrohError)
	// Set the download policy for this document
	SetDownloadPolicy(policy *DownloadPolicy) *IrohError
	// Set an entries on the doc via its key, hash, and size.
	SetHash(authorId *AuthorId, key []byte, hash *Hash, size uint64) *IrohError
	// Share this document with peers over a ticket.
	Share(mode ShareMode, addrOptions AddrInfoOptions) (*DocTicket, *IrohError)
	// Start to sync this document with a list of peers.
	StartSync(peers []*NodeAddr) *IrohError
	// Get status info for this document
	Status() (OpenState, *IrohError)
	// Subscribe to events for this document.
	Subscribe(cb SubscribeCallback) *IrohError
}

// A representation of a mutable, synchronizable key-value store.
type Doc struct {
	ffiObject FfiObject
}

// Close the document.
func (_self *Doc) CloseMe() *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_doc_close_me(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Delete entries that match the given `author` and key `prefix`.
//
// This inserts an empty entry with the key set to `prefix`, effectively clearing all other
// entries whose key starts with or is equal to the given `prefix`.
//
// Returns the number of entries deleted.
func (_self *Doc) Delete(authorId *AuthorId, prefix []byte) (uint64, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) C.uint64_t {
			res := C.ffi_iroh_ffi_rust_future_complete_u64(handle, status)
			return res
		},
		// liftFn
		func(ffi C.uint64_t) uint64 {
			return FfiConverterUint64INSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_doc_delete(
			_pointer, FfiConverterAuthorIdINSTANCE.Lower(authorId), FfiConverterBytesINSTANCE.Lower(prefix)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_u64(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_u64(handle)
		},
	)

	return res, err
}

// Export an entry as a file to a given absolute path
func (_self *Doc) ExportFile(entry *Entry, path string, cb *DocExportFileCallback) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_doc_export_file(
			_pointer, FfiConverterEntryINSTANCE.Lower(entry), FfiConverterStringINSTANCE.Lower(path), FfiConverterOptionalDocExportFileCallbackINSTANCE.Lower(cb)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Get the download policy for this document
func (_self *Doc) GetDownloadPolicy() (*DownloadPolicy, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *DownloadPolicy {
			return FfiConverterDownloadPolicyINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_doc_get_download_policy(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Get an entry for a key and author.
func (_self *Doc) GetExact(author *AuthorId, key []byte, includeEmpty bool) (**Entry, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) **Entry {
			return FfiConverterOptionalEntryINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_doc_get_exact(
			_pointer, FfiConverterAuthorIdINSTANCE.Lower(author), FfiConverterBytesINSTANCE.Lower(key), FfiConverterBoolINSTANCE.Lower(includeEmpty)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Get entries.
//
// Note: this allocates for each `Entry`, if you have many `Entry`s this may be a prohibitively large list.
// Please file an [issue](https://github.com/n0-computer/iroh-ffi/issues/new) if you run into this issue
func (_self *Doc) GetMany(query *Query) ([]*Entry, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []*Entry {
			return FfiConverterSequenceEntryINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_doc_get_many(
			_pointer, FfiConverterQueryINSTANCE.Lower(query)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Get the latest entry for a key and author.
func (_self *Doc) GetOne(query *Query) (**Entry, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) **Entry {
			return FfiConverterOptionalEntryINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_doc_get_one(
			_pointer, FfiConverterQueryINSTANCE.Lower(query)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Get sync peers for this document
func (_self *Doc) GetSyncPeers() (*[][]byte, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *[][]byte {
			return FfiConverterOptionalSequenceBytesINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_doc_get_sync_peers(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Get the document id of this doc.
func (_self *Doc) Id() string {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_doc_id(
				_pointer, _uniffiStatus),
		}
	}))
}

// Add an entry from an absolute file path
func (_self *Doc) ImportFile(author *AuthorId, key []byte, path string, inPlace bool, cb *DocImportFileCallback) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_doc_import_file(
			_pointer, FfiConverterAuthorIdINSTANCE.Lower(author), FfiConverterBytesINSTANCE.Lower(key), FfiConverterStringINSTANCE.Lower(path), FfiConverterBoolINSTANCE.Lower(inPlace), FfiConverterOptionalDocImportFileCallbackINSTANCE.Lower(cb)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Stop the live sync for this document.
func (_self *Doc) Leave() *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_doc_leave(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Set the content of a key to a byte array.
func (_self *Doc) SetBytes(authorId *AuthorId, key []byte, value []byte) (*Hash, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *Hash {
			return FfiConverterHashINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_doc_set_bytes(
			_pointer, FfiConverterAuthorIdINSTANCE.Lower(authorId), FfiConverterBytesINSTANCE.Lower(key), FfiConverterBytesINSTANCE.Lower(value)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Set the download policy for this document
func (_self *Doc) SetDownloadPolicy(policy *DownloadPolicy) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_doc_set_download_policy(
			_pointer, FfiConverterDownloadPolicyINSTANCE.Lower(policy)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Set an entries on the doc via its key, hash, and size.
func (_self *Doc) SetHash(authorId *AuthorId, key []byte, hash *Hash, size uint64) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_doc_set_hash(
			_pointer, FfiConverterAuthorIdINSTANCE.Lower(authorId), FfiConverterBytesINSTANCE.Lower(key), FfiConverterHashINSTANCE.Lower(hash), FfiConverterUint64INSTANCE.Lower(size)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Share this document with peers over a ticket.
func (_self *Doc) Share(mode ShareMode, addrOptions AddrInfoOptions) (*DocTicket, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *DocTicket {
			return FfiConverterDocTicketINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_doc_share(
			_pointer, FfiConverterShareModeINSTANCE.Lower(mode), FfiConverterAddrInfoOptionsINSTANCE.Lower(addrOptions)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Start to sync this document with a list of peers.
func (_self *Doc) StartSync(peers []*NodeAddr) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_doc_start_sync(
			_pointer, FfiConverterSequenceNodeAddrINSTANCE.Lower(peers)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Get status info for this document
func (_self *Doc) Status() (OpenState, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) OpenState {
			return FfiConverterOpenStateINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_doc_status(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Subscribe to events for this document.
func (_self *Doc) Subscribe(cb SubscribeCallback) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Doc")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_doc_subscribe(
			_pointer, FfiConverterSubscribeCallbackINSTANCE.Lower(cb)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}
func (object *Doc) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterDoc struct{}

var FfiConverterDocINSTANCE = FfiConverterDoc{}

func (c FfiConverterDoc) Lift(pointer unsafe.Pointer) *Doc {
	result := &Doc{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_doc(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_doc(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Doc).Destroy)
	return result
}

func (c FfiConverterDoc) Read(reader io.Reader) *Doc {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterDoc) Lower(value *Doc) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Doc")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterDoc) Write(writer io.Writer, value *Doc) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerDoc struct{}

func (_ FfiDestroyerDoc) Destroy(value *Doc) {
	value.Destroy()
}

// The `progress` method will be called for each `DocExportProgress` event that is
// emitted during a `doc.export_file()` call. Use the `DocExportProgress.type()`
// method to check the `DocExportProgressType`
type DocExportFileCallback interface {
	Progress(progress *DocExportProgress) *CallbackError
}

// The `progress` method will be called for each `DocExportProgress` event that is
// emitted during a `doc.export_file()` call. Use the `DocExportProgress.type()`
// method to check the `DocExportProgressType`
type DocExportFileCallbackImpl struct {
	ffiObject FfiObject
}

func (_self *DocExportFileCallbackImpl) Progress(progress *DocExportProgress) *CallbackError {
	_pointer := _self.ffiObject.incrementPointer("DocExportFileCallback")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[CallbackError](
		FfiConverterCallbackErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_docexportfilecallback_progress(
			_pointer, FfiConverterDocExportProgressINSTANCE.Lower(progress)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}
func (object *DocExportFileCallbackImpl) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterDocExportFileCallback struct {
	handleMap *concurrentHandleMap[DocExportFileCallback]
}

var FfiConverterDocExportFileCallbackINSTANCE = FfiConverterDocExportFileCallback{
	handleMap: newConcurrentHandleMap[DocExportFileCallback](),
}

func (c FfiConverterDocExportFileCallback) Lift(pointer unsafe.Pointer) DocExportFileCallback {
	result := &DocExportFileCallbackImpl{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_docexportfilecallback(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_docexportfilecallback(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*DocExportFileCallbackImpl).Destroy)
	return result
}

func (c FfiConverterDocExportFileCallback) Read(reader io.Reader) DocExportFileCallback {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterDocExportFileCallback) Lower(value DocExportFileCallback) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := unsafe.Pointer(uintptr(c.handleMap.insert(value)))
	return pointer

}

func (c FfiConverterDocExportFileCallback) Write(writer io.Writer, value DocExportFileCallback) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerDocExportFileCallback struct{}

func (_ FfiDestroyerDocExportFileCallback) Destroy(value DocExportFileCallback) {
	if val, ok := value.(*DocExportFileCallbackImpl); ok {
		val.Destroy()
	} else {
		panic("Expected *DocExportFileCallbackImpl")
	}
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceDocExportFileCallbackMethod0
func iroh_ffi_cgo_dispatchCallbackInterfaceDocExportFileCallbackMethod0(uniffiHandle C.uint64_t, progress unsafe.Pointer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterDocExportFileCallbackINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.iroh_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.Progress(
				FfiConverterDocExportProgressINSTANCE.Lift(progress),
			)

		if err != nil {
			// The only way to bypass an unexpected error is to bypass pointer to an empty
			// instance of the error
			if err.err == nil {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
				return
			}

			*callStatus = C.RustCallStatus{
				code:     C.int8_t(uniffiCallbackResultError),
				errorBuf: FfiConverterCallbackErrorINSTANCE.Lower(err),
			}
			return
		}

	}()
}

var UniffiVTableCallbackInterfaceDocExportFileCallbackINSTANCE = C.UniffiVTableCallbackInterfaceDocExportFileCallback{
	progress: (C.UniffiCallbackInterfaceDocExportFileCallbackMethod0)(C.iroh_ffi_cgo_dispatchCallbackInterfaceDocExportFileCallbackMethod0),

	uniffiFree: (C.UniffiCallbackInterfaceFree)(C.iroh_ffi_cgo_dispatchCallbackInterfaceDocExportFileCallbackFree),
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceDocExportFileCallbackFree
func iroh_ffi_cgo_dispatchCallbackInterfaceDocExportFileCallbackFree(handle C.uint64_t) {
	FfiConverterDocExportFileCallbackINSTANCE.handleMap.remove(uint64(handle))
}

func (c FfiConverterDocExportFileCallback) register() {
	C.uniffi_iroh_ffi_fn_init_callback_vtable_docexportfilecallback(&UniffiVTableCallbackInterfaceDocExportFileCallbackINSTANCE)
}

// Progress updates for the doc import file operation.
type DocExportProgressInterface interface {
	// Return the `DocExportProgressAbort`
	AsAbort() DocExportProgressAbort
	// Return the `DocExportProgressFound` event
	AsFound() DocExportProgressFound
	// Return the `DocExportProgressProgress` event
	AsProgress() DocExportProgressProgress
	// Get the type of event
	Type() DocExportProgressType
}

// Progress updates for the doc import file operation.
type DocExportProgress struct {
	ffiObject FfiObject
}

// Return the `DocExportProgressAbort`
func (_self *DocExportProgress) AsAbort() DocExportProgressAbort {
	_pointer := _self.ffiObject.incrementPointer("*DocExportProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDocExportProgressAbortINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_docexportprogress_as_abort(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `DocExportProgressFound` event
func (_self *DocExportProgress) AsFound() DocExportProgressFound {
	_pointer := _self.ffiObject.incrementPointer("*DocExportProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDocExportProgressFoundINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_docexportprogress_as_found(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `DocExportProgressProgress` event
func (_self *DocExportProgress) AsProgress() DocExportProgressProgress {
	_pointer := _self.ffiObject.incrementPointer("*DocExportProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDocExportProgressProgressINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_docexportprogress_as_progress(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the type of event
func (_self *DocExportProgress) Type() DocExportProgressType {
	_pointer := _self.ffiObject.incrementPointer("*DocExportProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDocExportProgressTypeINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_docexportprogress_type(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *DocExportProgress) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterDocExportProgress struct{}

var FfiConverterDocExportProgressINSTANCE = FfiConverterDocExportProgress{}

func (c FfiConverterDocExportProgress) Lift(pointer unsafe.Pointer) *DocExportProgress {
	result := &DocExportProgress{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_docexportprogress(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_docexportprogress(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*DocExportProgress).Destroy)
	return result
}

func (c FfiConverterDocExportProgress) Read(reader io.Reader) *DocExportProgress {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterDocExportProgress) Lower(value *DocExportProgress) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*DocExportProgress")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterDocExportProgress) Write(writer io.Writer, value *DocExportProgress) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerDocExportProgress struct{}

func (_ FfiDestroyerDocExportProgress) Destroy(value *DocExportProgress) {
	value.Destroy()
}

// The `progress` method will be called for each `DocImportProgress` event that is
// emitted during a `doc.import_file()` call. Use the `DocImportProgress.type()`
// method to check the `DocImportProgressType`
type DocImportFileCallback interface {
	Progress(progress *DocImportProgress) *CallbackError
}

// The `progress` method will be called for each `DocImportProgress` event that is
// emitted during a `doc.import_file()` call. Use the `DocImportProgress.type()`
// method to check the `DocImportProgressType`
type DocImportFileCallbackImpl struct {
	ffiObject FfiObject
}

func (_self *DocImportFileCallbackImpl) Progress(progress *DocImportProgress) *CallbackError {
	_pointer := _self.ffiObject.incrementPointer("DocImportFileCallback")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[CallbackError](
		FfiConverterCallbackErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_docimportfilecallback_progress(
			_pointer, FfiConverterDocImportProgressINSTANCE.Lower(progress)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}
func (object *DocImportFileCallbackImpl) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterDocImportFileCallback struct {
	handleMap *concurrentHandleMap[DocImportFileCallback]
}

var FfiConverterDocImportFileCallbackINSTANCE = FfiConverterDocImportFileCallback{
	handleMap: newConcurrentHandleMap[DocImportFileCallback](),
}

func (c FfiConverterDocImportFileCallback) Lift(pointer unsafe.Pointer) DocImportFileCallback {
	result := &DocImportFileCallbackImpl{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_docimportfilecallback(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_docimportfilecallback(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*DocImportFileCallbackImpl).Destroy)
	return result
}

func (c FfiConverterDocImportFileCallback) Read(reader io.Reader) DocImportFileCallback {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterDocImportFileCallback) Lower(value DocImportFileCallback) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := unsafe.Pointer(uintptr(c.handleMap.insert(value)))
	return pointer

}

func (c FfiConverterDocImportFileCallback) Write(writer io.Writer, value DocImportFileCallback) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerDocImportFileCallback struct{}

func (_ FfiDestroyerDocImportFileCallback) Destroy(value DocImportFileCallback) {
	if val, ok := value.(*DocImportFileCallbackImpl); ok {
		val.Destroy()
	} else {
		panic("Expected *DocImportFileCallbackImpl")
	}
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceDocImportFileCallbackMethod0
func iroh_ffi_cgo_dispatchCallbackInterfaceDocImportFileCallbackMethod0(uniffiHandle C.uint64_t, progress unsafe.Pointer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterDocImportFileCallbackINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.iroh_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.Progress(
				FfiConverterDocImportProgressINSTANCE.Lift(progress),
			)

		if err != nil {
			// The only way to bypass an unexpected error is to bypass pointer to an empty
			// instance of the error
			if err.err == nil {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
				return
			}

			*callStatus = C.RustCallStatus{
				code:     C.int8_t(uniffiCallbackResultError),
				errorBuf: FfiConverterCallbackErrorINSTANCE.Lower(err),
			}
			return
		}

	}()
}

var UniffiVTableCallbackInterfaceDocImportFileCallbackINSTANCE = C.UniffiVTableCallbackInterfaceDocImportFileCallback{
	progress: (C.UniffiCallbackInterfaceDocImportFileCallbackMethod0)(C.iroh_ffi_cgo_dispatchCallbackInterfaceDocImportFileCallbackMethod0),

	uniffiFree: (C.UniffiCallbackInterfaceFree)(C.iroh_ffi_cgo_dispatchCallbackInterfaceDocImportFileCallbackFree),
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceDocImportFileCallbackFree
func iroh_ffi_cgo_dispatchCallbackInterfaceDocImportFileCallbackFree(handle C.uint64_t) {
	FfiConverterDocImportFileCallbackINSTANCE.handleMap.remove(uint64(handle))
}

func (c FfiConverterDocImportFileCallback) register() {
	C.uniffi_iroh_ffi_fn_init_callback_vtable_docimportfilecallback(&UniffiVTableCallbackInterfaceDocImportFileCallbackINSTANCE)
}

// Progress updates for the doc import file operation.
type DocImportProgressInterface interface {
	// Return the `DocImportProgressAbort`
	AsAbort() DocImportProgressAbort
	// Return the `DocImportProgressAllDone`
	AsAllDone() DocImportProgressAllDone
	// Return the `DocImportProgressFound` event
	AsFound() DocImportProgressFound
	// Return the `DocImportProgressDone` event
	AsIngestDone() DocImportProgressIngestDone
	// Return the `DocImportProgressProgress` event
	AsProgress() DocImportProgressProgress
	// Get the type of event
	Type() DocImportProgressType
}

// Progress updates for the doc import file operation.
type DocImportProgress struct {
	ffiObject FfiObject
}

// Return the `DocImportProgressAbort`
func (_self *DocImportProgress) AsAbort() DocImportProgressAbort {
	_pointer := _self.ffiObject.incrementPointer("*DocImportProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDocImportProgressAbortINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_docimportprogress_as_abort(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `DocImportProgressAllDone`
func (_self *DocImportProgress) AsAllDone() DocImportProgressAllDone {
	_pointer := _self.ffiObject.incrementPointer("*DocImportProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDocImportProgressAllDoneINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_docimportprogress_as_all_done(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `DocImportProgressFound` event
func (_self *DocImportProgress) AsFound() DocImportProgressFound {
	_pointer := _self.ffiObject.incrementPointer("*DocImportProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDocImportProgressFoundINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_docimportprogress_as_found(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `DocImportProgressDone` event
func (_self *DocImportProgress) AsIngestDone() DocImportProgressIngestDone {
	_pointer := _self.ffiObject.incrementPointer("*DocImportProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDocImportProgressIngestDoneINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_docimportprogress_as_ingest_done(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `DocImportProgressProgress` event
func (_self *DocImportProgress) AsProgress() DocImportProgressProgress {
	_pointer := _self.ffiObject.incrementPointer("*DocImportProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDocImportProgressProgressINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_docimportprogress_as_progress(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the type of event
func (_self *DocImportProgress) Type() DocImportProgressType {
	_pointer := _self.ffiObject.incrementPointer("*DocImportProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDocImportProgressTypeINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_docimportprogress_type(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *DocImportProgress) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterDocImportProgress struct{}

var FfiConverterDocImportProgressINSTANCE = FfiConverterDocImportProgress{}

func (c FfiConverterDocImportProgress) Lift(pointer unsafe.Pointer) *DocImportProgress {
	result := &DocImportProgress{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_docimportprogress(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_docimportprogress(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*DocImportProgress).Destroy)
	return result
}

func (c FfiConverterDocImportProgress) Read(reader io.Reader) *DocImportProgress {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterDocImportProgress) Lower(value *DocImportProgress) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*DocImportProgress")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterDocImportProgress) Write(writer io.Writer, value *DocImportProgress) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerDocImportProgress struct{}

func (_ FfiDestroyerDocImportProgress) Destroy(value *DocImportProgress) {
	value.Destroy()
}

// Contains both a key (either secret or public) to a document, and a list of peers to join.
type DocTicketInterface interface {
}

// Contains both a key (either secret or public) to a document, and a list of peers to join.
type DocTicket struct {
	ffiObject FfiObject
}

func NewDocTicket(str string) (*DocTicket, *IrohError) {
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_docticket_new(FfiConverterStringINSTANCE.Lower(str), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *DocTicket
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterDocTicketINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

func (_self *DocTicket) String() string {
	_pointer := _self.ffiObject.incrementPointer("*DocTicket")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_docticket_uniffi_trait_display(
				_pointer, _uniffiStatus),
		}
	}))
}

func (object *DocTicket) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterDocTicket struct{}

var FfiConverterDocTicketINSTANCE = FfiConverterDocTicket{}

func (c FfiConverterDocTicket) Lift(pointer unsafe.Pointer) *DocTicket {
	result := &DocTicket{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_docticket(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_docticket(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*DocTicket).Destroy)
	return result
}

func (c FfiConverterDocTicket) Read(reader io.Reader) *DocTicket {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterDocTicket) Lower(value *DocTicket) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*DocTicket")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterDocTicket) Write(writer io.Writer, value *DocTicket) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerDocTicket struct{}

func (_ FfiDestroyerDocTicket) Destroy(value *DocTicket) {
	value.Destroy()
}

// Iroh docs client.
type DocsInterface interface {
	// Create a new doc.
	Create() (*Doc, *IrohError)
	// Delete a document from the local node.
	//
	// This is a destructive operation. Both the document secret key and all entries in the
	// document will be permanently deleted from the node's storage. Content blobs will be deleted
	// through garbage collection unless they are referenced from another document or tag.
	DropDoc(docId string) *IrohError
	// Join and sync with an already existing document.
	Join(ticket *DocTicket) (*Doc, *IrohError)
	// Join and sync with an already existing document and subscribe to events on that document.
	JoinAndSubscribe(ticket *DocTicket, cb SubscribeCallback) (*Doc, *IrohError)
	// List all the docs we have access to on this node.
	List() ([]NamespaceAndCapability, *IrohError)
	// Get a [`Doc`].
	//
	// Returns None if the document cannot be found.
	Open(id string) (**Doc, *IrohError)
}

// Iroh docs client.
type Docs struct {
	ffiObject FfiObject
}

// Create a new doc.
func (_self *Docs) Create() (*Doc, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Docs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *Doc {
			return FfiConverterDocINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_docs_create(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Delete a document from the local node.
//
// This is a destructive operation. Both the document secret key and all entries in the
// document will be permanently deleted from the node's storage. Content blobs will be deleted
// through garbage collection unless they are referenced from another document or tag.
func (_self *Docs) DropDoc(docId string) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Docs")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_docs_drop_doc(
			_pointer, FfiConverterStringINSTANCE.Lower(docId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Join and sync with an already existing document.
func (_self *Docs) Join(ticket *DocTicket) (*Doc, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Docs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *Doc {
			return FfiConverterDocINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_docs_join(
			_pointer, FfiConverterDocTicketINSTANCE.Lower(ticket)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Join and sync with an already existing document and subscribe to events on that document.
func (_self *Docs) JoinAndSubscribe(ticket *DocTicket, cb SubscribeCallback) (*Doc, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Docs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *Doc {
			return FfiConverterDocINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_docs_join_and_subscribe(
			_pointer, FfiConverterDocTicketINSTANCE.Lower(ticket), FfiConverterSubscribeCallbackINSTANCE.Lower(cb)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// List all the docs we have access to on this node.
func (_self *Docs) List() ([]NamespaceAndCapability, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Docs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []NamespaceAndCapability {
			return FfiConverterSequenceNamespaceAndCapabilityINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_docs_list(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Get a [`Doc`].
//
// Returns None if the document cannot be found.
func (_self *Docs) Open(id string) (**Doc, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Docs")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) **Doc {
			return FfiConverterOptionalDocINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_docs_open(
			_pointer, FfiConverterStringINSTANCE.Lower(id)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}
func (object *Docs) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterDocs struct{}

var FfiConverterDocsINSTANCE = FfiConverterDocs{}

func (c FfiConverterDocs) Lift(pointer unsafe.Pointer) *Docs {
	result := &Docs{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_docs(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_docs(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Docs).Destroy)
	return result
}

func (c FfiConverterDocs) Read(reader io.Reader) *Docs {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterDocs) Lower(value *Docs) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Docs")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterDocs) Write(writer io.Writer, value *Docs) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerDocs struct{}

func (_ FfiDestroyerDocs) Destroy(value *Docs) {
	value.Destroy()
}

// The `progress` method will be called for each `DownloadProgress` event that is emitted during
// a `node.blobs_download`. Use the `DownloadProgress.type()` method to check the
// `DownloadProgressType` of the event.
type DownloadCallback interface {
	Progress(progress *DownloadProgress) *CallbackError
}

// The `progress` method will be called for each `DownloadProgress` event that is emitted during
// a `node.blobs_download`. Use the `DownloadProgress.type()` method to check the
// `DownloadProgressType` of the event.
type DownloadCallbackImpl struct {
	ffiObject FfiObject
}

func (_self *DownloadCallbackImpl) Progress(progress *DownloadProgress) *CallbackError {
	_pointer := _self.ffiObject.incrementPointer("DownloadCallback")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[CallbackError](
		FfiConverterCallbackErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_downloadcallback_progress(
			_pointer, FfiConverterDownloadProgressINSTANCE.Lower(progress)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}
func (object *DownloadCallbackImpl) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterDownloadCallback struct {
	handleMap *concurrentHandleMap[DownloadCallback]
}

var FfiConverterDownloadCallbackINSTANCE = FfiConverterDownloadCallback{
	handleMap: newConcurrentHandleMap[DownloadCallback](),
}

func (c FfiConverterDownloadCallback) Lift(pointer unsafe.Pointer) DownloadCallback {
	result := &DownloadCallbackImpl{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_downloadcallback(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_downloadcallback(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*DownloadCallbackImpl).Destroy)
	return result
}

func (c FfiConverterDownloadCallback) Read(reader io.Reader) DownloadCallback {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterDownloadCallback) Lower(value DownloadCallback) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := unsafe.Pointer(uintptr(c.handleMap.insert(value)))
	return pointer

}

func (c FfiConverterDownloadCallback) Write(writer io.Writer, value DownloadCallback) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerDownloadCallback struct{}

func (_ FfiDestroyerDownloadCallback) Destroy(value DownloadCallback) {
	if val, ok := value.(*DownloadCallbackImpl); ok {
		val.Destroy()
	} else {
		panic("Expected *DownloadCallbackImpl")
	}
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceDownloadCallbackMethod0
func iroh_ffi_cgo_dispatchCallbackInterfaceDownloadCallbackMethod0(uniffiHandle C.uint64_t, progress unsafe.Pointer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterDownloadCallbackINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.iroh_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.Progress(
				FfiConverterDownloadProgressINSTANCE.Lift(progress),
			)

		if err != nil {
			// The only way to bypass an unexpected error is to bypass pointer to an empty
			// instance of the error
			if err.err == nil {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
				return
			}

			*callStatus = C.RustCallStatus{
				code:     C.int8_t(uniffiCallbackResultError),
				errorBuf: FfiConverterCallbackErrorINSTANCE.Lower(err),
			}
			return
		}

	}()
}

var UniffiVTableCallbackInterfaceDownloadCallbackINSTANCE = C.UniffiVTableCallbackInterfaceDownloadCallback{
	progress: (C.UniffiCallbackInterfaceDownloadCallbackMethod0)(C.iroh_ffi_cgo_dispatchCallbackInterfaceDownloadCallbackMethod0),

	uniffiFree: (C.UniffiCallbackInterfaceFree)(C.iroh_ffi_cgo_dispatchCallbackInterfaceDownloadCallbackFree),
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceDownloadCallbackFree
func iroh_ffi_cgo_dispatchCallbackInterfaceDownloadCallbackFree(handle C.uint64_t) {
	FfiConverterDownloadCallbackINSTANCE.handleMap.remove(uint64(handle))
}

func (c FfiConverterDownloadCallback) register() {
	C.uniffi_iroh_ffi_fn_init_callback_vtable_downloadcallback(&UniffiVTableCallbackInterfaceDownloadCallbackINSTANCE)
}

// Download policy to decide which content blobs shall be downloaded.
type DownloadPolicyInterface interface {
}

// Download policy to decide which content blobs shall be downloaded.
type DownloadPolicy struct {
	ffiObject FfiObject
}

// Download everything
func DownloadPolicyEverything() *DownloadPolicy {
	return FfiConverterDownloadPolicyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_downloadpolicy_everything(_uniffiStatus)
	}))
}

// Download everything except keys that match the given filters
func DownloadPolicyEverythingExcept(filters []*FilterKind) *DownloadPolicy {
	return FfiConverterDownloadPolicyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_downloadpolicy_everything_except(FfiConverterSequenceFilterKindINSTANCE.Lower(filters), _uniffiStatus)
	}))
}

// Download nothing
func DownloadPolicyNothing() *DownloadPolicy {
	return FfiConverterDownloadPolicyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_downloadpolicy_nothing(_uniffiStatus)
	}))
}

// Download nothing except keys that match the given filters
func DownloadPolicyNothingExcept(filters []*FilterKind) *DownloadPolicy {
	return FfiConverterDownloadPolicyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_downloadpolicy_nothing_except(FfiConverterSequenceFilterKindINSTANCE.Lower(filters), _uniffiStatus)
	}))
}

func (object *DownloadPolicy) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterDownloadPolicy struct{}

var FfiConverterDownloadPolicyINSTANCE = FfiConverterDownloadPolicy{}

func (c FfiConverterDownloadPolicy) Lift(pointer unsafe.Pointer) *DownloadPolicy {
	result := &DownloadPolicy{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_downloadpolicy(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_downloadpolicy(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*DownloadPolicy).Destroy)
	return result
}

func (c FfiConverterDownloadPolicy) Read(reader io.Reader) *DownloadPolicy {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterDownloadPolicy) Lower(value *DownloadPolicy) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*DownloadPolicy")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterDownloadPolicy) Write(writer io.Writer, value *DownloadPolicy) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerDownloadPolicy struct{}

func (_ FfiDestroyerDownloadPolicy) Destroy(value *DownloadPolicy) {
	value.Destroy()
}

// Progress updates for the get operation.
type DownloadProgressInterface interface {
	// Return the `DownloadProgressAbort` event
	AsAbort() DownloadProgressAbort
	// Return the `DownloadProgressAllDone` event
	AsAllDone() DownloadProgressAllDone
	// Return the `DownloadProgressDone` event
	AsDone() DownloadProgressDone
	// Return the `DownloadProgressFound` event
	AsFound() DownloadProgressFound
	// Return the `DownloadProgressFoundHashSeq` event
	AsFoundHashSeq() DownloadProgressFoundHashSeq
	// Return the `DownloadProgressFoundLocal` event
	AsFoundLocal() DownloadProgressFoundLocal
	// Return the `DownloadProgressProgress` event
	AsProgress() DownloadProgressProgress
	// Get the type of event
	// note that there is no `as_connected` method, as the `Connected` event has no associated data
	Type() DownloadProgressType
}

// Progress updates for the get operation.
type DownloadProgress struct {
	ffiObject FfiObject
}

// Return the `DownloadProgressAbort` event
func (_self *DownloadProgress) AsAbort() DownloadProgressAbort {
	_pointer := _self.ffiObject.incrementPointer("*DownloadProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDownloadProgressAbortINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_downloadprogress_as_abort(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `DownloadProgressAllDone` event
func (_self *DownloadProgress) AsAllDone() DownloadProgressAllDone {
	_pointer := _self.ffiObject.incrementPointer("*DownloadProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDownloadProgressAllDoneINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_downloadprogress_as_all_done(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `DownloadProgressDone` event
func (_self *DownloadProgress) AsDone() DownloadProgressDone {
	_pointer := _self.ffiObject.incrementPointer("*DownloadProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDownloadProgressDoneINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_downloadprogress_as_done(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `DownloadProgressFound` event
func (_self *DownloadProgress) AsFound() DownloadProgressFound {
	_pointer := _self.ffiObject.incrementPointer("*DownloadProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDownloadProgressFoundINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_downloadprogress_as_found(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `DownloadProgressFoundHashSeq` event
func (_self *DownloadProgress) AsFoundHashSeq() DownloadProgressFoundHashSeq {
	_pointer := _self.ffiObject.incrementPointer("*DownloadProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDownloadProgressFoundHashSeqINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_downloadprogress_as_found_hash_seq(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `DownloadProgressFoundLocal` event
func (_self *DownloadProgress) AsFoundLocal() DownloadProgressFoundLocal {
	_pointer := _self.ffiObject.incrementPointer("*DownloadProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDownloadProgressFoundLocalINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_downloadprogress_as_found_local(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the `DownloadProgressProgress` event
func (_self *DownloadProgress) AsProgress() DownloadProgressProgress {
	_pointer := _self.ffiObject.incrementPointer("*DownloadProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDownloadProgressProgressINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_downloadprogress_as_progress(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the type of event
// note that there is no `as_connected` method, as the `Connected` event has no associated data
func (_self *DownloadProgress) Type() DownloadProgressType {
	_pointer := _self.ffiObject.incrementPointer("*DownloadProgress")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDownloadProgressTypeINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_downloadprogress_type(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *DownloadProgress) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterDownloadProgress struct{}

var FfiConverterDownloadProgressINSTANCE = FfiConverterDownloadProgress{}

func (c FfiConverterDownloadProgress) Lift(pointer unsafe.Pointer) *DownloadProgress {
	result := &DownloadProgress{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_downloadprogress(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_downloadprogress(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*DownloadProgress).Destroy)
	return result
}

func (c FfiConverterDownloadProgress) Read(reader io.Reader) *DownloadProgress {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterDownloadProgress) Lower(value *DownloadProgress) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*DownloadProgress")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterDownloadProgress) Write(writer io.Writer, value *DownloadProgress) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerDownloadProgress struct{}

func (_ FfiDestroyerDownloadProgress) Destroy(value *DownloadProgress) {
	value.Destroy()
}

type EndpointInterface interface {
	Connect(nodeAddr *NodeAddr, alpn []byte) (*Connection, *IrohError)
	// The string representation of this endpoint's NodeId.
	NodeId() (string, *IrohError)
}
type Endpoint struct {
	ffiObject FfiObject
}

func (_self *Endpoint) Connect(nodeAddr *NodeAddr, alpn []byte) (*Connection, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Endpoint")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *Connection {
			return FfiConverterConnectionINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_endpoint_connect(
			_pointer, FfiConverterNodeAddrINSTANCE.Lower(nodeAddr), FfiConverterBytesINSTANCE.Lower(alpn)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// The string representation of this endpoint's NodeId.
func (_self *Endpoint) NodeId() (string, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Endpoint")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_endpoint_node_id(
				_pointer, _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}
func (object *Endpoint) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterEndpoint struct{}

var FfiConverterEndpointINSTANCE = FfiConverterEndpoint{}

func (c FfiConverterEndpoint) Lift(pointer unsafe.Pointer) *Endpoint {
	result := &Endpoint{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_endpoint(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_endpoint(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Endpoint).Destroy)
	return result
}

func (c FfiConverterEndpoint) Read(reader io.Reader) *Endpoint {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterEndpoint) Lower(value *Endpoint) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Endpoint")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterEndpoint) Write(writer io.Writer, value *Endpoint) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerEndpoint struct{}

func (_ FfiDestroyerEndpoint) Destroy(value *Endpoint) {
	value.Destroy()
}

// A single entry in a [`Doc`]
//
// An entry is identified by a key, its [`AuthorId`], and the [`Doc`]'s
// namespace id. Its value is the 32-byte BLAKE3 [`hash`]
// of the entry's content data, the size of this content data, and a timestamp.
type EntryInterface interface {
	// Get the [`AuthorId`] of this entry.
	Author() *AuthorId
	// Get the content_hash of this entry.
	ContentHash() *Hash
	// Get the content_length of this entry.
	ContentLen() uint64
	// Get the key of this entry.
	Key() []byte
	// Get the namespace id of this entry.
	Namespace() string
	// Get the timestamp when this entry was written.
	Timestamp() uint64
}

// A single entry in a [`Doc`]
//
// An entry is identified by a key, its [`AuthorId`], and the [`Doc`]'s
// namespace id. Its value is the 32-byte BLAKE3 [`hash`]
// of the entry's content data, the size of this content data, and a timestamp.
type Entry struct {
	ffiObject FfiObject
}

// Get the [`AuthorId`] of this entry.
func (_self *Entry) Author() *AuthorId {
	_pointer := _self.ffiObject.incrementPointer("*Entry")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterAuthorIdINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_entry_author(
			_pointer, _uniffiStatus)
	}))
}

// Get the content_hash of this entry.
func (_self *Entry) ContentHash() *Hash {
	_pointer := _self.ffiObject.incrementPointer("*Entry")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterHashINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_entry_content_hash(
			_pointer, _uniffiStatus)
	}))
}

// Get the content_length of this entry.
func (_self *Entry) ContentLen() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Entry")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_iroh_ffi_fn_method_entry_content_len(
			_pointer, _uniffiStatus)
	}))
}

// Get the key of this entry.
func (_self *Entry) Key() []byte {
	_pointer := _self.ffiObject.incrementPointer("*Entry")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBytesINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_entry_key(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the namespace id of this entry.
func (_self *Entry) Namespace() string {
	_pointer := _self.ffiObject.incrementPointer("*Entry")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_entry_namespace(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the timestamp when this entry was written.
func (_self *Entry) Timestamp() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Entry")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_iroh_ffi_fn_method_entry_timestamp(
			_pointer, _uniffiStatus)
	}))
}
func (object *Entry) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterEntry struct{}

var FfiConverterEntryINSTANCE = FfiConverterEntry{}

func (c FfiConverterEntry) Lift(pointer unsafe.Pointer) *Entry {
	result := &Entry{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_entry(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_entry(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Entry).Destroy)
	return result
}

func (c FfiConverterEntry) Read(reader io.Reader) *Entry {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterEntry) Lower(value *Entry) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Entry")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterEntry) Write(writer io.Writer, value *Entry) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerEntry struct{}

func (_ FfiDestroyerEntry) Destroy(value *Entry) {
	value.Destroy()
}

// Filter strategy used in download policies.
type FilterKindInterface interface {
	// Verifies whether this filter matches a given key
	Matches(key []byte) bool
}

// Filter strategy used in download policies.
type FilterKind struct {
	ffiObject FfiObject
}

// Returns a FilterKind that matches if the contained bytes and the key are the same.
func FilterKindExact(key []byte) *FilterKind {
	return FfiConverterFilterKindINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_filterkind_exact(FfiConverterBytesINSTANCE.Lower(key), _uniffiStatus)
	}))
}

// Returns a FilterKind that matches if the contained bytes are a prefix of the key.
func FilterKindPrefix(prefix []byte) *FilterKind {
	return FfiConverterFilterKindINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_filterkind_prefix(FfiConverterBytesINSTANCE.Lower(prefix), _uniffiStatus)
	}))
}

// Verifies whether this filter matches a given key
func (_self *FilterKind) Matches(key []byte) bool {
	_pointer := _self.ffiObject.incrementPointer("*FilterKind")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_iroh_ffi_fn_method_filterkind_matches(
			_pointer, FfiConverterBytesINSTANCE.Lower(key), _uniffiStatus)
	}))
}
func (object *FilterKind) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterFilterKind struct{}

var FfiConverterFilterKindINSTANCE = FfiConverterFilterKind{}

func (c FfiConverterFilterKind) Lift(pointer unsafe.Pointer) *FilterKind {
	result := &FilterKind{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_filterkind(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_filterkind(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*FilterKind).Destroy)
	return result
}

func (c FfiConverterFilterKind) Read(reader io.Reader) *FilterKind {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterFilterKind) Lower(value *FilterKind) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*FilterKind")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterFilterKind) Write(writer io.Writer, value *FilterKind) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerFilterKind struct{}

func (_ FfiDestroyerFilterKind) Destroy(value *FilterKind) {
	value.Destroy()
}

// Iroh gossip client.
type GossipInterface interface {
	Subscribe(topic []byte, bootstrap []string, cb GossipMessageCallback) (*Sender, *IrohError)
}

// Iroh gossip client.
type Gossip struct {
	ffiObject FfiObject
}

func (_self *Gossip) Subscribe(topic []byte, bootstrap []string, cb GossipMessageCallback) (*Sender, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Gossip")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *Sender {
			return FfiConverterSenderINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_gossip_subscribe(
			_pointer, FfiConverterBytesINSTANCE.Lower(topic), FfiConverterSequenceStringINSTANCE.Lower(bootstrap), FfiConverterGossipMessageCallbackINSTANCE.Lower(cb)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}
func (object *Gossip) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterGossip struct{}

var FfiConverterGossipINSTANCE = FfiConverterGossip{}

func (c FfiConverterGossip) Lift(pointer unsafe.Pointer) *Gossip {
	result := &Gossip{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_gossip(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_gossip(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Gossip).Destroy)
	return result
}

func (c FfiConverterGossip) Read(reader io.Reader) *Gossip {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterGossip) Lower(value *Gossip) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Gossip")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterGossip) Write(writer io.Writer, value *Gossip) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerGossip struct{}

func (_ FfiDestroyerGossip) Destroy(value *Gossip) {
	value.Destroy()
}

type GossipMessageCallback interface {
	OnMessage(msg *Message) *CallbackError
}
type GossipMessageCallbackImpl struct {
	ffiObject FfiObject
}

func (_self *GossipMessageCallbackImpl) OnMessage(msg *Message) *CallbackError {
	_pointer := _self.ffiObject.incrementPointer("GossipMessageCallback")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[CallbackError](
		FfiConverterCallbackErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_gossipmessagecallback_on_message(
			_pointer, FfiConverterMessageINSTANCE.Lower(msg)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}
func (object *GossipMessageCallbackImpl) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterGossipMessageCallback struct {
	handleMap *concurrentHandleMap[GossipMessageCallback]
}

var FfiConverterGossipMessageCallbackINSTANCE = FfiConverterGossipMessageCallback{
	handleMap: newConcurrentHandleMap[GossipMessageCallback](),
}

func (c FfiConverterGossipMessageCallback) Lift(pointer unsafe.Pointer) GossipMessageCallback {
	result := &GossipMessageCallbackImpl{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_gossipmessagecallback(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_gossipmessagecallback(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*GossipMessageCallbackImpl).Destroy)
	return result
}

func (c FfiConverterGossipMessageCallback) Read(reader io.Reader) GossipMessageCallback {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterGossipMessageCallback) Lower(value GossipMessageCallback) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := unsafe.Pointer(uintptr(c.handleMap.insert(value)))
	return pointer

}

func (c FfiConverterGossipMessageCallback) Write(writer io.Writer, value GossipMessageCallback) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerGossipMessageCallback struct{}

func (_ FfiDestroyerGossipMessageCallback) Destroy(value GossipMessageCallback) {
	if val, ok := value.(*GossipMessageCallbackImpl); ok {
		val.Destroy()
	} else {
		panic("Expected *GossipMessageCallbackImpl")
	}
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceGossipMessageCallbackMethod0
func iroh_ffi_cgo_dispatchCallbackInterfaceGossipMessageCallbackMethod0(uniffiHandle C.uint64_t, msg unsafe.Pointer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterGossipMessageCallbackINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.iroh_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.OnMessage(
				FfiConverterMessageINSTANCE.Lift(msg),
			)

		if err != nil {
			// The only way to bypass an unexpected error is to bypass pointer to an empty
			// instance of the error
			if err.err == nil {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
				return
			}

			*callStatus = C.RustCallStatus{
				code:     C.int8_t(uniffiCallbackResultError),
				errorBuf: FfiConverterCallbackErrorINSTANCE.Lower(err),
			}
			return
		}

	}()
}

var UniffiVTableCallbackInterfaceGossipMessageCallbackINSTANCE = C.UniffiVTableCallbackInterfaceGossipMessageCallback{
	onMessage: (C.UniffiCallbackInterfaceGossipMessageCallbackMethod0)(C.iroh_ffi_cgo_dispatchCallbackInterfaceGossipMessageCallbackMethod0),

	uniffiFree: (C.UniffiCallbackInterfaceFree)(C.iroh_ffi_cgo_dispatchCallbackInterfaceGossipMessageCallbackFree),
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceGossipMessageCallbackFree
func iroh_ffi_cgo_dispatchCallbackInterfaceGossipMessageCallbackFree(handle C.uint64_t) {
	FfiConverterGossipMessageCallbackINSTANCE.handleMap.remove(uint64(handle))
}

func (c FfiConverterGossipMessageCallback) register() {
	C.uniffi_iroh_ffi_fn_init_callback_vtable_gossipmessagecallback(&UniffiVTableCallbackInterfaceGossipMessageCallbackINSTANCE)
}

// Hash type used throughout Iroh. A blake3 hash.
type HashInterface interface {
	// Returns true if the Hash's have the same value
	Equal(other *Hash) bool
	// Bytes of the hash.
	ToBytes() []byte
	// Convert the hash to a hex string.
	ToHex() string
}

// Hash type used throughout Iroh. A blake3 hash.
type Hash struct {
	ffiObject FfiObject
}

// Calculate the hash of the provide bytes.
func NewHash(buf []byte) *Hash {
	return FfiConverterHashINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_hash_new(FfiConverterBytesINSTANCE.Lower(buf), _uniffiStatus)
	}))
}

// Create a `Hash` from its raw bytes representation.
func HashFromBytes(bytes []byte) (*Hash, *IrohError) {
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_hash_from_bytes(FfiConverterBytesINSTANCE.Lower(bytes), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Hash
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterHashINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Make a Hash from hex string
func HashFromString(s string) (*Hash, *IrohError) {
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_hash_from_string(FfiConverterStringINSTANCE.Lower(s), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Hash
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterHashINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Returns true if the Hash's have the same value
func (_self *Hash) Equal(other *Hash) bool {
	_pointer := _self.ffiObject.incrementPointer("*Hash")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_iroh_ffi_fn_method_hash_equal(
			_pointer, FfiConverterHashINSTANCE.Lower(other), _uniffiStatus)
	}))
}

// Bytes of the hash.
func (_self *Hash) ToBytes() []byte {
	_pointer := _self.ffiObject.incrementPointer("*Hash")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBytesINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_hash_to_bytes(
				_pointer, _uniffiStatus),
		}
	}))
}

// Convert the hash to a hex string.
func (_self *Hash) ToHex() string {
	_pointer := _self.ffiObject.incrementPointer("*Hash")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_hash_to_hex(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Hash) String() string {
	_pointer := _self.ffiObject.incrementPointer("*Hash")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_hash_uniffi_trait_display(
				_pointer, _uniffiStatus),
		}
	}))
}

func (object *Hash) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterHash struct{}

var FfiConverterHashINSTANCE = FfiConverterHash{}

func (c FfiConverterHash) Lift(pointer unsafe.Pointer) *Hash {
	result := &Hash{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_hash(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_hash(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Hash).Destroy)
	return result
}

func (c FfiConverterHash) Read(reader io.Reader) *Hash {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterHash) Lower(value *Hash) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Hash")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterHash) Write(writer io.Writer, value *Hash) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerHash struct{}

func (_ FfiDestroyerHash) Destroy(value *Hash) {
	value.Destroy()
}

// An Iroh node. Allows you to sync, store, and transfer data.
type IrohInterface interface {
	// Access to gossip specific funtionaliy.
	Authors() *Authors
	// Access to blob specific funtionaliy.
	Blobs() *Blobs
	// Access to docs specific funtionaliy.
	Docs() *Docs
	// Access to gossip specific funtionaliy.
	Gossip() *Gossip
	// Access to blob specific funtionaliy.
	Net() *Net
	// Access to node specific funtionaliy.
	Node() *Node
	// Access to tags specific funtionaliy.
	Tags() *Tags
}

// An Iroh node. Allows you to sync, store, and transfer data.
type Iroh struct {
	ffiObject FfiObject
}

// Create a new iroh node.
//
// All data will be only persistet in memory.
func IrohMemory() (*Iroh, *IrohError) {
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *Iroh {
			return FfiConverterIrohINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_constructor_iroh_memory(),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Create a new in memory iroh node with options.
func IrohMemoryWithOptions(options NodeOptions) (*Iroh, *IrohError) {
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *Iroh {
			return FfiConverterIrohINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_constructor_iroh_memory_with_options(FfiConverterNodeOptionsINSTANCE.Lower(options)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Create a new iroh node.
//
// The `path` param should be a directory where we can store or load
// iroh data from a previous session.
func IrohPersistent(path string) (*Iroh, *IrohError) {
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *Iroh {
			return FfiConverterIrohINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_constructor_iroh_persistent(FfiConverterStringINSTANCE.Lower(path)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Create a new iroh node with options.
func IrohPersistentWithOptions(path string, options NodeOptions) (*Iroh, *IrohError) {
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *Iroh {
			return FfiConverterIrohINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_constructor_iroh_persistent_with_options(FfiConverterStringINSTANCE.Lower(path), FfiConverterNodeOptionsINSTANCE.Lower(options)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// Access to gossip specific funtionaliy.
func (_self *Iroh) Authors() *Authors {
	_pointer := _self.ffiObject.incrementPointer("*Iroh")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterAuthorsINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_iroh_authors(
			_pointer, _uniffiStatus)
	}))
}

// Access to blob specific funtionaliy.
func (_self *Iroh) Blobs() *Blobs {
	_pointer := _self.ffiObject.incrementPointer("*Iroh")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBlobsINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_iroh_blobs(
			_pointer, _uniffiStatus)
	}))
}

// Access to docs specific funtionaliy.
func (_self *Iroh) Docs() *Docs {
	_pointer := _self.ffiObject.incrementPointer("*Iroh")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterDocsINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_iroh_docs(
			_pointer, _uniffiStatus)
	}))
}

// Access to gossip specific funtionaliy.
func (_self *Iroh) Gossip() *Gossip {
	_pointer := _self.ffiObject.incrementPointer("*Iroh")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterGossipINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_iroh_gossip(
			_pointer, _uniffiStatus)
	}))
}

// Access to blob specific funtionaliy.
func (_self *Iroh) Net() *Net {
	_pointer := _self.ffiObject.incrementPointer("*Iroh")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterNetINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_iroh_net(
			_pointer, _uniffiStatus)
	}))
}

// Access to node specific funtionaliy.
func (_self *Iroh) Node() *Node {
	_pointer := _self.ffiObject.incrementPointer("*Iroh")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterNodeINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_iroh_node(
			_pointer, _uniffiStatus)
	}))
}

// Access to tags specific funtionaliy.
func (_self *Iroh) Tags() *Tags {
	_pointer := _self.ffiObject.incrementPointer("*Iroh")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTagsINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_iroh_tags(
			_pointer, _uniffiStatus)
	}))
}
func (object *Iroh) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterIroh struct{}

var FfiConverterIrohINSTANCE = FfiConverterIroh{}

func (c FfiConverterIroh) Lift(pointer unsafe.Pointer) *Iroh {
	result := &Iroh{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_iroh(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_iroh(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Iroh).Destroy)
	return result
}

func (c FfiConverterIroh) Read(reader io.Reader) *Iroh {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterIroh) Lower(value *Iroh) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Iroh")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterIroh) Write(writer io.Writer, value *Iroh) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerIroh struct{}

func (_ FfiDestroyerIroh) Destroy(value *Iroh) {
	value.Destroy()
}

// An Error.
type IrohErrorInterface interface {
	Message() string
}

// An Error.
type IrohError struct {
	ffiObject FfiObject
}

func (_self *IrohError) Message() string {
	_pointer := _self.ffiObject.incrementPointer("*IrohError")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_iroherror_message(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *IrohError) DebugString() string {
	_pointer := _self.ffiObject.incrementPointer("*IrohError")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_iroherror_uniffi_trait_debug(
				_pointer, _uniffiStatus),
		}
	}))
}

func (object *IrohError) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterIrohError struct{}

var FfiConverterIrohErrorINSTANCE = FfiConverterIrohError{}

func (_self IrohError) Error() string {
	return "IrohError"
}

func (_self *IrohError) AsError() error {
	if _self == nil {
		return nil
	} else {
		return _self
	}
}
func (c FfiConverterIrohError) Lift(pointer unsafe.Pointer) *IrohError {
	result := &IrohError{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_iroherror(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_iroherror(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*IrohError).Destroy)
	return result
}

func (c FfiConverterIrohError) Read(reader io.Reader) *IrohError {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterIrohError) Lower(value *IrohError) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*IrohError")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterIrohError) Write(writer io.Writer, value *IrohError) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerIrohError struct{}

func (_ FfiDestroyerIrohError) Destroy(value *IrohError) {
	value.Destroy()
}

// Events informing about actions of the live sync progress
type LiveEventInterface interface {
	// For `LiveEventType::ContentReady`, returns a Hash
	AsContentReady() *Hash
	// For `LiveEventType::InsertLocal`, returns an Entry
	AsInsertLocal() *Entry
	// For `LiveEventType::InsertRemote`, returns an InsertRemoteEvent
	AsInsertRemote() InsertRemoteEvent
	// For `LiveEventType::NeighborDown`, returns a PublicKey
	AsNeighborDown() *PublicKey
	// For `LiveEventType::NeighborUp`, returns a PublicKey
	AsNeighborUp() *PublicKey
	// For `LiveEventType::SyncFinished`, returns a SyncEvent
	AsSyncFinished() SyncEvent
	// The type LiveEvent
	Type() LiveEventType
}

// Events informing about actions of the live sync progress
type LiveEvent struct {
	ffiObject FfiObject
}

// For `LiveEventType::ContentReady`, returns a Hash
func (_self *LiveEvent) AsContentReady() *Hash {
	_pointer := _self.ffiObject.incrementPointer("*LiveEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterHashINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_liveevent_as_content_ready(
			_pointer, _uniffiStatus)
	}))
}

// For `LiveEventType::InsertLocal`, returns an Entry
func (_self *LiveEvent) AsInsertLocal() *Entry {
	_pointer := _self.ffiObject.incrementPointer("*LiveEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterEntryINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_liveevent_as_insert_local(
			_pointer, _uniffiStatus)
	}))
}

// For `LiveEventType::InsertRemote`, returns an InsertRemoteEvent
func (_self *LiveEvent) AsInsertRemote() InsertRemoteEvent {
	_pointer := _self.ffiObject.incrementPointer("*LiveEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterInsertRemoteEventINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_liveevent_as_insert_remote(
				_pointer, _uniffiStatus),
		}
	}))
}

// For `LiveEventType::NeighborDown`, returns a PublicKey
func (_self *LiveEvent) AsNeighborDown() *PublicKey {
	_pointer := _self.ffiObject.incrementPointer("*LiveEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterPublicKeyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_liveevent_as_neighbor_down(
			_pointer, _uniffiStatus)
	}))
}

// For `LiveEventType::NeighborUp`, returns a PublicKey
func (_self *LiveEvent) AsNeighborUp() *PublicKey {
	_pointer := _self.ffiObject.incrementPointer("*LiveEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterPublicKeyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_liveevent_as_neighbor_up(
			_pointer, _uniffiStatus)
	}))
}

// For `LiveEventType::SyncFinished`, returns a SyncEvent
func (_self *LiveEvent) AsSyncFinished() SyncEvent {
	_pointer := _self.ffiObject.incrementPointer("*LiveEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSyncEventINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_liveevent_as_sync_finished(
				_pointer, _uniffiStatus),
		}
	}))
}

// The type LiveEvent
func (_self *LiveEvent) Type() LiveEventType {
	_pointer := _self.ffiObject.incrementPointer("*LiveEvent")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterLiveEventTypeINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_liveevent_type(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *LiveEvent) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterLiveEvent struct{}

var FfiConverterLiveEventINSTANCE = FfiConverterLiveEvent{}

func (c FfiConverterLiveEvent) Lift(pointer unsafe.Pointer) *LiveEvent {
	result := &LiveEvent{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_liveevent(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_liveevent(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*LiveEvent).Destroy)
	return result
}

func (c FfiConverterLiveEvent) Read(reader io.Reader) *LiveEvent {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterLiveEvent) Lower(value *LiveEvent) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*LiveEvent")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterLiveEvent) Write(writer io.Writer, value *LiveEvent) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerLiveEvent struct{}

func (_ FfiDestroyerLiveEvent) Destroy(value *LiveEvent) {
	value.Destroy()
}

// Gossip message
type MessageInterface interface {
	AsError() string
	AsJoined() []string
	AsNeighborDown() string
	AsNeighborUp() string
	AsReceived() MessageContent
	Type() MessageType
}

// Gossip message
type Message struct {
	ffiObject FfiObject
}

func (_self *Message) AsError() string {
	_pointer := _self.ffiObject.incrementPointer("*Message")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_message_as_error(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Message) AsJoined() []string {
	_pointer := _self.ffiObject.incrementPointer("*Message")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_message_as_joined(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Message) AsNeighborDown() string {
	_pointer := _self.ffiObject.incrementPointer("*Message")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_message_as_neighbor_down(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Message) AsNeighborUp() string {
	_pointer := _self.ffiObject.incrementPointer("*Message")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_message_as_neighbor_up(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Message) AsReceived() MessageContent {
	_pointer := _self.ffiObject.incrementPointer("*Message")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterMessageContentINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_message_as_received(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Message) Type() MessageType {
	_pointer := _self.ffiObject.incrementPointer("*Message")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterMessageTypeINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_message_type(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *Message) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterMessage struct{}

var FfiConverterMessageINSTANCE = FfiConverterMessage{}

func (c FfiConverterMessage) Lift(pointer unsafe.Pointer) *Message {
	result := &Message{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_message(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_message(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Message).Destroy)
	return result
}

func (c FfiConverterMessage) Read(reader io.Reader) *Message {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterMessage) Lower(value *Message) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Message")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterMessage) Write(writer io.Writer, value *Message) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerMessage struct{}

func (_ FfiDestroyerMessage) Destroy(value *Message) {
	value.Destroy()
}

// Iroh net client.
type NetInterface interface {
	// Add a known node address to the node.
	AddNodeAddr(addr *NodeAddr) *IrohError
	// Get the relay server we are connected to.
	HomeRelay() (*string, *IrohError)
	// Return the [`NodeAddr`] for this node.
	NodeAddr() (*NodeAddr, *IrohError)
	// The string representation of the PublicKey of this node.
	NodeId() (string, *IrohError)
	// Return connection information on the currently running node.
	RemoteInfo(nodeId *PublicKey) (*RemoteInfo, *IrohError)
	// Return `ConnectionInfo`s for each connection we have to another iroh node.
	RemoteInfoList() ([]RemoteInfo, *IrohError)
}

// Iroh net client.
type Net struct {
	ffiObject FfiObject
}

// Add a known node address to the node.
func (_self *Net) AddNodeAddr(addr *NodeAddr) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Net")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_net_add_node_addr(
			_pointer, FfiConverterNodeAddrINSTANCE.Lower(addr)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Get the relay server we are connected to.
func (_self *Net) HomeRelay() (*string, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Net")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *string {
			return FfiConverterOptionalStringINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_net_home_relay(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Return the [`NodeAddr`] for this node.
func (_self *Net) NodeAddr() (*NodeAddr, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Net")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *NodeAddr {
			return FfiConverterNodeAddrINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_net_node_addr(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}

// The string representation of the PublicKey of this node.
func (_self *Net) NodeId() (string, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Net")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) string {
			return FfiConverterStringINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_net_node_id(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Return connection information on the currently running node.
func (_self *Net) RemoteInfo(nodeId *PublicKey) (*RemoteInfo, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Net")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *RemoteInfo {
			return FfiConverterOptionalRemoteInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_net_remote_info(
			_pointer, FfiConverterPublicKeyINSTANCE.Lower(nodeId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Return `ConnectionInfo`s for each connection we have to another iroh node.
func (_self *Net) RemoteInfoList() ([]RemoteInfo, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Net")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []RemoteInfo {
			return FfiConverterSequenceRemoteInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_net_remote_info_list(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}
func (object *Net) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterNet struct{}

var FfiConverterNetINSTANCE = FfiConverterNet{}

func (c FfiConverterNet) Lift(pointer unsafe.Pointer) *Net {
	result := &Net{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_net(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_net(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Net).Destroy)
	return result
}

func (c FfiConverterNet) Read(reader io.Reader) *Net {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterNet) Lower(value *Net) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Net")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterNet) Write(writer io.Writer, value *Net) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerNet struct{}

func (_ FfiDestroyerNet) Destroy(value *Net) {
	value.Destroy()
}

// Iroh node client.
type NodeInterface interface {
	Endpoint() *Endpoint
	// Shutdown this iroh node.
	Shutdown() *IrohError
	// Get statistics of the running node.
	Stats() (map[string]CounterStats, *IrohError)
	// Get status information about a node
	Status() (*NodeStatus, *IrohError)
}

// Iroh node client.
type Node struct {
	ffiObject FfiObject
}

func (_self *Node) Endpoint() *Endpoint {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterEndpointINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_node_endpoint(
			_pointer, _uniffiStatus)
	}))
}

// Shutdown this iroh node.
func (_self *Node) Shutdown() *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_node_shutdown(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Get statistics of the running node.
func (_self *Node) Stats() (map[string]CounterStats, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) map[string]CounterStats {
			return FfiConverterMapStringCounterStatsINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_node_stats(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

// Get status information about a node
func (_self *Node) Status() (*NodeStatus, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_iroh_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *NodeStatus {
			return FfiConverterNodeStatusINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_node_status(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_pointer(handle)
		},
	)

	return res, err
}
func (object *Node) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterNode struct{}

var FfiConverterNodeINSTANCE = FfiConverterNode{}

func (c FfiConverterNode) Lift(pointer unsafe.Pointer) *Node {
	result := &Node{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_node(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_node(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Node).Destroy)
	return result
}

func (c FfiConverterNode) Read(reader io.Reader) *Node {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterNode) Lower(value *Node) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Node")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterNode) Write(writer io.Writer, value *Node) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerNode struct{}

func (_ FfiDestroyerNode) Destroy(value *Node) {
	value.Destroy()
}

// A peer and it's addressing information.
type NodeAddrInterface interface {
	// Get the direct addresses of this peer.
	DirectAddresses() []string
	// Returns true if both NodeAddr's have the same values
	Equal(other *NodeAddr) bool
	// Get the home relay URL for this peer
	RelayUrl() *string
}

// A peer and it's addressing information.
type NodeAddr struct {
	ffiObject FfiObject
}

// Create a new [`NodeAddr`] with empty [`AddrInfo`].
func NewNodeAddr(nodeId *PublicKey, derpUrl *string, addresses []string) *NodeAddr {
	return FfiConverterNodeAddrINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_nodeaddr_new(FfiConverterPublicKeyINSTANCE.Lower(nodeId), FfiConverterOptionalStringINSTANCE.Lower(derpUrl), FfiConverterSequenceStringINSTANCE.Lower(addresses), _uniffiStatus)
	}))
}

// Get the direct addresses of this peer.
func (_self *NodeAddr) DirectAddresses() []string {
	_pointer := _self.ffiObject.incrementPointer("*NodeAddr")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_nodeaddr_direct_addresses(
				_pointer, _uniffiStatus),
		}
	}))
}

// Returns true if both NodeAddr's have the same values
func (_self *NodeAddr) Equal(other *NodeAddr) bool {
	_pointer := _self.ffiObject.incrementPointer("*NodeAddr")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_iroh_ffi_fn_method_nodeaddr_equal(
			_pointer, FfiConverterNodeAddrINSTANCE.Lower(other), _uniffiStatus)
	}))
}

// Get the home relay URL for this peer
func (_self *NodeAddr) RelayUrl() *string {
	_pointer := _self.ffiObject.incrementPointer("*NodeAddr")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_nodeaddr_relay_url(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *NodeAddr) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterNodeAddr struct{}

var FfiConverterNodeAddrINSTANCE = FfiConverterNodeAddr{}

func (c FfiConverterNodeAddr) Lift(pointer unsafe.Pointer) *NodeAddr {
	result := &NodeAddr{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_nodeaddr(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_nodeaddr(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*NodeAddr).Destroy)
	return result
}

func (c FfiConverterNodeAddr) Read(reader io.Reader) *NodeAddr {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterNodeAddr) Lower(value *NodeAddr) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*NodeAddr")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterNodeAddr) Write(writer io.Writer, value *NodeAddr) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerNodeAddr struct{}

func (_ FfiDestroyerNodeAddr) Destroy(value *NodeAddr) {
	value.Destroy()
}

// The response to a status request
type NodeStatusInterface interface {
	// The bound listening addresses of the node
	ListenAddrs() []string
	// The node id and socket addresses of this node.
	NodeAddr() *NodeAddr
	// The address of the RPC of the node
	RpcAddr() *string
	// The version of the node
	Version() string
}

// The response to a status request
type NodeStatus struct {
	ffiObject FfiObject
}

// The bound listening addresses of the node
func (_self *NodeStatus) ListenAddrs() []string {
	_pointer := _self.ffiObject.incrementPointer("*NodeStatus")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_nodestatus_listen_addrs(
				_pointer, _uniffiStatus),
		}
	}))
}

// The node id and socket addresses of this node.
func (_self *NodeStatus) NodeAddr() *NodeAddr {
	_pointer := _self.ffiObject.incrementPointer("*NodeStatus")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterNodeAddrINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_nodestatus_node_addr(
			_pointer, _uniffiStatus)
	}))
}

// The address of the RPC of the node
func (_self *NodeStatus) RpcAddr() *string {
	_pointer := _self.ffiObject.incrementPointer("*NodeStatus")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_nodestatus_rpc_addr(
				_pointer, _uniffiStatus),
		}
	}))
}

// The version of the node
func (_self *NodeStatus) Version() string {
	_pointer := _self.ffiObject.incrementPointer("*NodeStatus")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_nodestatus_version(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *NodeStatus) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterNodeStatus struct{}

var FfiConverterNodeStatusINSTANCE = FfiConverterNodeStatus{}

func (c FfiConverterNodeStatus) Lift(pointer unsafe.Pointer) *NodeStatus {
	result := &NodeStatus{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_nodestatus(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_nodestatus(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*NodeStatus).Destroy)
	return result
}

func (c FfiConverterNodeStatus) Read(reader io.Reader) *NodeStatus {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterNodeStatus) Lower(value *NodeStatus) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*NodeStatus")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterNodeStatus) Write(writer io.Writer, value *NodeStatus) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerNodeStatus struct{}

func (_ FfiDestroyerNodeStatus) Destroy(value *NodeStatus) {
	value.Destroy()
}

// A token containing information for establishing a connection to a node.
//
// This allows establishing a connection to the node in most circumstances where it is
// possible to do so.
//
// It is a single item which can be easily serialized and deserialized.
type NodeTicketInterface interface {
	// The [`NodeAddr`] of the provider for this ticket.
	NodeAddr() *NodeAddr
}

// A token containing information for establishing a connection to a node.
//
// This allows establishing a connection to the node in most circumstances where it is
// possible to do so.
//
// It is a single item which can be easily serialized and deserialized.
type NodeTicket struct {
	ffiObject FfiObject
}

// Wrap the given [`NodeAddr`] as a [`NodeTicket`].
//
// The returned ticket can easily be deserialized using its string presentation, and
// later parsed again using [`Self::parse`].
func NewNodeTicket(addr *NodeAddr) (*NodeTicket, *IrohError) {
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_nodeticket_new(FfiConverterNodeAddrINSTANCE.Lower(addr), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *NodeTicket
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterNodeTicketINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Parse back a [`NodeTicket`] from its string presentation.
func NodeTicketParse(str string) (*NodeTicket, *IrohError) {
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_nodeticket_parse(FfiConverterStringINSTANCE.Lower(str), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *NodeTicket
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterNodeTicketINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// The [`NodeAddr`] of the provider for this ticket.
func (_self *NodeTicket) NodeAddr() *NodeAddr {
	_pointer := _self.ffiObject.incrementPointer("*NodeTicket")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterNodeAddrINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_nodeticket_node_addr(
			_pointer, _uniffiStatus)
	}))
}

func (_self *NodeTicket) String() string {
	_pointer := _self.ffiObject.incrementPointer("*NodeTicket")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_nodeticket_uniffi_trait_display(
				_pointer, _uniffiStatus),
		}
	}))
}

func (object *NodeTicket) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterNodeTicket struct{}

var FfiConverterNodeTicketINSTANCE = FfiConverterNodeTicket{}

func (c FfiConverterNodeTicket) Lift(pointer unsafe.Pointer) *NodeTicket {
	result := &NodeTicket{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_nodeticket(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_nodeticket(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*NodeTicket).Destroy)
	return result
}

func (c FfiConverterNodeTicket) Read(reader io.Reader) *NodeTicket {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterNodeTicket) Lower(value *NodeTicket) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*NodeTicket")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterNodeTicket) Write(writer io.Writer, value *NodeTicket) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerNodeTicket struct{}

func (_ FfiDestroyerNodeTicket) Destroy(value *NodeTicket) {
	value.Destroy()
}

type ProtocolCreator interface {
	Create(endpoint *Endpoint) ProtocolHandler
}
type ProtocolCreatorImpl struct {
	ffiObject FfiObject
}

func (_self *ProtocolCreatorImpl) Create(endpoint *Endpoint) ProtocolHandler {
	_pointer := _self.ffiObject.incrementPointer("ProtocolCreator")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterProtocolHandlerINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_method_protocolcreator_create(
			_pointer, FfiConverterEndpointINSTANCE.Lower(endpoint), _uniffiStatus)
	}))
}
func (object *ProtocolCreatorImpl) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterProtocolCreator struct {
	handleMap *concurrentHandleMap[ProtocolCreator]
}

var FfiConverterProtocolCreatorINSTANCE = FfiConverterProtocolCreator{
	handleMap: newConcurrentHandleMap[ProtocolCreator](),
}

func (c FfiConverterProtocolCreator) Lift(pointer unsafe.Pointer) ProtocolCreator {
	result := &ProtocolCreatorImpl{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_protocolcreator(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_protocolcreator(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*ProtocolCreatorImpl).Destroy)
	return result
}

func (c FfiConverterProtocolCreator) Read(reader io.Reader) ProtocolCreator {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterProtocolCreator) Lower(value ProtocolCreator) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := unsafe.Pointer(uintptr(c.handleMap.insert(value)))
	return pointer

}

func (c FfiConverterProtocolCreator) Write(writer io.Writer, value ProtocolCreator) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerProtocolCreator struct{}

func (_ FfiDestroyerProtocolCreator) Destroy(value ProtocolCreator) {
	if val, ok := value.(*ProtocolCreatorImpl); ok {
		val.Destroy()
	} else {
		panic("Expected *ProtocolCreatorImpl")
	}
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceProtocolCreatorMethod0
func iroh_ffi_cgo_dispatchCallbackInterfaceProtocolCreatorMethod0(uniffiHandle C.uint64_t, endpoint unsafe.Pointer, uniffiOutReturn *unsafe.Pointer, callStatus *C.RustCallStatus) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterProtocolCreatorINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	res :=
		uniffiObj.Create(
			FfiConverterEndpointINSTANCE.Lift(endpoint),
		)

	*uniffiOutReturn = FfiConverterProtocolHandlerINSTANCE.Lower(res)
}

var UniffiVTableCallbackInterfaceProtocolCreatorINSTANCE = C.UniffiVTableCallbackInterfaceProtocolCreator{
	create: (C.UniffiCallbackInterfaceProtocolCreatorMethod0)(C.iroh_ffi_cgo_dispatchCallbackInterfaceProtocolCreatorMethod0),

	uniffiFree: (C.UniffiCallbackInterfaceFree)(C.iroh_ffi_cgo_dispatchCallbackInterfaceProtocolCreatorFree),
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceProtocolCreatorFree
func iroh_ffi_cgo_dispatchCallbackInterfaceProtocolCreatorFree(handle C.uint64_t) {
	FfiConverterProtocolCreatorINSTANCE.handleMap.remove(uint64(handle))
}

func (c FfiConverterProtocolCreator) register() {
	C.uniffi_iroh_ffi_fn_init_callback_vtable_protocolcreator(&UniffiVTableCallbackInterfaceProtocolCreatorINSTANCE)
}

type ProtocolHandler interface {
	Accept(conn *Connection) *CallbackError
	Shutdown()
}
type ProtocolHandlerImpl struct {
	ffiObject FfiObject
}

func (_self *ProtocolHandlerImpl) Accept(conn *Connection) *CallbackError {
	_pointer := _self.ffiObject.incrementPointer("ProtocolHandler")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[CallbackError](
		FfiConverterCallbackErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_protocolhandler_accept(
			_pointer, FfiConverterConnectionINSTANCE.Lower(conn)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

func (_self *ProtocolHandlerImpl) Shutdown() {
	_pointer := _self.ffiObject.incrementPointer("ProtocolHandler")
	defer _self.ffiObject.decrementPointer()
	uniffiRustCallAsync[struct{}](
		nil,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_protocolhandler_shutdown(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

}
func (object *ProtocolHandlerImpl) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterProtocolHandler struct {
	handleMap *concurrentHandleMap[ProtocolHandler]
}

var FfiConverterProtocolHandlerINSTANCE = FfiConverterProtocolHandler{
	handleMap: newConcurrentHandleMap[ProtocolHandler](),
}

func (c FfiConverterProtocolHandler) Lift(pointer unsafe.Pointer) ProtocolHandler {
	result := &ProtocolHandlerImpl{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_protocolhandler(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_protocolhandler(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*ProtocolHandlerImpl).Destroy)
	return result
}

func (c FfiConverterProtocolHandler) Read(reader io.Reader) ProtocolHandler {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterProtocolHandler) Lower(value ProtocolHandler) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := unsafe.Pointer(uintptr(c.handleMap.insert(value)))
	return pointer

}

func (c FfiConverterProtocolHandler) Write(writer io.Writer, value ProtocolHandler) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerProtocolHandler struct{}

func (_ FfiDestroyerProtocolHandler) Destroy(value ProtocolHandler) {
	if val, ok := value.(*ProtocolHandlerImpl); ok {
		val.Destroy()
	} else {
		panic("Expected *ProtocolHandlerImpl")
	}
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceProtocolHandlerMethod0
func iroh_ffi_cgo_dispatchCallbackInterfaceProtocolHandlerMethod0(uniffiHandle C.uint64_t, conn unsafe.Pointer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterProtocolHandlerINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.iroh_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.Accept(
				FfiConverterConnectionINSTANCE.Lift(conn),
			)

		if err != nil {
			// The only way to bypass an unexpected error is to bypass pointer to an empty
			// instance of the error
			if err.err == nil {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
				return
			}

			*callStatus = C.RustCallStatus{
				code:     C.int8_t(uniffiCallbackResultError),
				errorBuf: FfiConverterCallbackErrorINSTANCE.Lower(err),
			}
			return
		}

	}()
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceProtocolHandlerMethod1
func iroh_ffi_cgo_dispatchCallbackInterfaceProtocolHandlerMethod1(uniffiHandle C.uint64_t, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterProtocolHandlerINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.iroh_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		defer func() {
			result <- *asyncResult
		}()

		uniffiObj.Shutdown()

	}()
}

var UniffiVTableCallbackInterfaceProtocolHandlerINSTANCE = C.UniffiVTableCallbackInterfaceProtocolHandler{
	accept:   (C.UniffiCallbackInterfaceProtocolHandlerMethod0)(C.iroh_ffi_cgo_dispatchCallbackInterfaceProtocolHandlerMethod0),
	shutdown: (C.UniffiCallbackInterfaceProtocolHandlerMethod1)(C.iroh_ffi_cgo_dispatchCallbackInterfaceProtocolHandlerMethod1),

	uniffiFree: (C.UniffiCallbackInterfaceFree)(C.iroh_ffi_cgo_dispatchCallbackInterfaceProtocolHandlerFree),
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceProtocolHandlerFree
func iroh_ffi_cgo_dispatchCallbackInterfaceProtocolHandlerFree(handle C.uint64_t) {
	FfiConverterProtocolHandlerINSTANCE.handleMap.remove(uint64(handle))
}

func (c FfiConverterProtocolHandler) register() {
	C.uniffi_iroh_ffi_fn_init_callback_vtable_protocolhandler(&UniffiVTableCallbackInterfaceProtocolHandlerINSTANCE)
}

// A public key.
//
// The key itself is just a 32 byte array, but a key has associated crypto
// information that is cached for performance reasons.
type PublicKeyInterface interface {
	// Returns true if the PublicKeys are equal
	Equal(other *PublicKey) bool
	// Convert to a base32 string limited to the first 10 bytes for a friendly string
	// representation of the key.
	FmtShort() string
	// Express the PublicKey as a byte array
	ToBytes() []byte
}

// A public key.
//
// The key itself is just a 32 byte array, but a key has associated crypto
// information that is cached for performance reasons.
type PublicKey struct {
	ffiObject FfiObject
}

// Make a PublicKey from byte array
func PublicKeyFromBytes(bytes []byte) (*PublicKey, *IrohError) {
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_publickey_from_bytes(FfiConverterBytesINSTANCE.Lower(bytes), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *PublicKey
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterPublicKeyINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Make a PublicKey from base32 string
func PublicKeyFromString(s string) (*PublicKey, *IrohError) {
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_publickey_from_string(FfiConverterStringINSTANCE.Lower(s), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *PublicKey
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterPublicKeyINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Returns true if the PublicKeys are equal
func (_self *PublicKey) Equal(other *PublicKey) bool {
	_pointer := _self.ffiObject.incrementPointer("*PublicKey")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_iroh_ffi_fn_method_publickey_equal(
			_pointer, FfiConverterPublicKeyINSTANCE.Lower(other), _uniffiStatus)
	}))
}

// Convert to a base32 string limited to the first 10 bytes for a friendly string
// representation of the key.
func (_self *PublicKey) FmtShort() string {
	_pointer := _self.ffiObject.incrementPointer("*PublicKey")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_publickey_fmt_short(
				_pointer, _uniffiStatus),
		}
	}))
}

// Express the PublicKey as a byte array
func (_self *PublicKey) ToBytes() []byte {
	_pointer := _self.ffiObject.incrementPointer("*PublicKey")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBytesINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_publickey_to_bytes(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *PublicKey) String() string {
	_pointer := _self.ffiObject.incrementPointer("*PublicKey")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_publickey_uniffi_trait_display(
				_pointer, _uniffiStatus),
		}
	}))
}

func (object *PublicKey) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterPublicKey struct{}

var FfiConverterPublicKeyINSTANCE = FfiConverterPublicKey{}

func (c FfiConverterPublicKey) Lift(pointer unsafe.Pointer) *PublicKey {
	result := &PublicKey{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_publickey(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_publickey(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*PublicKey).Destroy)
	return result
}

func (c FfiConverterPublicKey) Read(reader io.Reader) *PublicKey {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterPublicKey) Lower(value *PublicKey) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*PublicKey")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterPublicKey) Write(writer io.Writer, value *PublicKey) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerPublicKey struct{}

func (_ FfiDestroyerPublicKey) Destroy(value *PublicKey) {
	value.Destroy()
}

// Build a Query to search for an entry or entries in a doc.
//
// Use this with `QueryOptions` to determine sorting, grouping, and pagination.
type QueryInterface interface {
	// Get the limit for this query (max. number of entries to emit).
	Limit() *uint64
	// Get the offset for this query (number of entries to skip at the beginning).
	Offset() uint64
}

// Build a Query to search for an entry or entries in a doc.
//
// Use this with `QueryOptions` to determine sorting, grouping, and pagination.
type Query struct {
	ffiObject FfiObject
}

// Query all records.
//
// If `opts` is `None`, the default values will be used:
// sort_by: SortBy::AuthorKey
// direction: SortDirection::Asc
// offset: None
// limit: None
func QueryAll(opts *QueryOptions) *Query {
	return FfiConverterQueryINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_query_all(FfiConverterOptionalQueryOptionsINSTANCE.Lower(opts), _uniffiStatus)
	}))
}

// Query all entries for by a single author.
//
// If `opts` is `None`, the default values will be used:
// sort_by: SortBy::AuthorKey
// direction: SortDirection::Asc
// offset: None
// limit: None
func QueryAuthor(author *AuthorId, opts *QueryOptions) *Query {
	return FfiConverterQueryINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_query_author(FfiConverterAuthorIdINSTANCE.Lower(author), FfiConverterOptionalQueryOptionsINSTANCE.Lower(opts), _uniffiStatus)
	}))
}

// Create a Query for a single key and author.
func QueryAuthorKeyExact(author *AuthorId, key []byte) *Query {
	return FfiConverterQueryINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_query_author_key_exact(FfiConverterAuthorIdINSTANCE.Lower(author), FfiConverterBytesINSTANCE.Lower(key), _uniffiStatus)
	}))
}

// Create a query for all entries of a single author with a given key prefix.
//
// If `opts` is `None`, the default values will be used:
// direction: SortDirection::Asc
// offset: None
// limit: None
func QueryAuthorKeyPrefix(author *AuthorId, prefix []byte, opts *QueryOptions) *Query {
	return FfiConverterQueryINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_query_author_key_prefix(FfiConverterAuthorIdINSTANCE.Lower(author), FfiConverterBytesINSTANCE.Lower(prefix), FfiConverterOptionalQueryOptionsINSTANCE.Lower(opts), _uniffiStatus)
	}))
}

// Query all entries that have an exact key.
//
// If `opts` is `None`, the default values will be used:
// sort_by: SortBy::AuthorKey
// direction: SortDirection::Asc
// offset: None
// limit: None
func QueryKeyExact(key []byte, opts *QueryOptions) *Query {
	return FfiConverterQueryINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_query_key_exact(FfiConverterBytesINSTANCE.Lower(key), FfiConverterOptionalQueryOptionsINSTANCE.Lower(opts), _uniffiStatus)
	}))
}

// Create a query for all entries with a given key prefix.
//
// If `opts` is `None`, the default values will be used:
// sort_by: SortBy::AuthorKey
// direction: SortDirection::Asc
// offset: None
// limit: None
func QueryKeyPrefix(prefix []byte, opts *QueryOptions) *Query {
	return FfiConverterQueryINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_query_key_prefix(FfiConverterBytesINSTANCE.Lower(prefix), FfiConverterOptionalQueryOptionsINSTANCE.Lower(opts), _uniffiStatus)
	}))
}

// Query only the latest entry for each key, omitting older entries if the entry was written
// to by multiple authors.
//
// If `opts` is `None`, the default values will be used:
// direction: SortDirection::Asc
// offset: None
// limit: None
func QuerySingleLatestPerKey(opts *QueryOptions) *Query {
	return FfiConverterQueryINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_query_single_latest_per_key(FfiConverterOptionalQueryOptionsINSTANCE.Lower(opts), _uniffiStatus)
	}))
}

// Query exactly the key, but only the latest entry for it, omitting older entries if the entry was written
// to by multiple authors.
func QuerySingleLatestPerKeyExact(key []byte) *Query {
	return FfiConverterQueryINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_query_single_latest_per_key_exact(FfiConverterBytesINSTANCE.Lower(key), _uniffiStatus)
	}))
}

// Query only the latest entry for each key, with this prefix, omitting older entries if the entry was written
// to by multiple authors.
//
// If `opts` is `None`, the default values will be used:
// direction: SortDirection::Asc
// offset: None
// limit: None
func QuerySingleLatestPerKeyPrefix(prefix []byte, opts *QueryOptions) *Query {
	return FfiConverterQueryINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_query_single_latest_per_key_prefix(FfiConverterBytesINSTANCE.Lower(prefix), FfiConverterOptionalQueryOptionsINSTANCE.Lower(opts), _uniffiStatus)
	}))
}

// Get the limit for this query (max. number of entries to emit).
func (_self *Query) Limit() *uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Query")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_method_query_limit(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the offset for this query (number of entries to skip at the beginning).
func (_self *Query) Offset() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Query")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_iroh_ffi_fn_method_query_offset(
			_pointer, _uniffiStatus)
	}))
}
func (object *Query) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterQuery struct{}

var FfiConverterQueryINSTANCE = FfiConverterQuery{}

func (c FfiConverterQuery) Lift(pointer unsafe.Pointer) *Query {
	result := &Query{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_query(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_query(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Query).Destroy)
	return result
}

func (c FfiConverterQuery) Read(reader io.Reader) *Query {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterQuery) Lower(value *Query) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Query")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterQuery) Write(writer io.Writer, value *Query) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerQuery struct{}

func (_ FfiDestroyerQuery) Destroy(value *Query) {
	value.Destroy()
}

// A chunk range specification as a sequence of chunk offsets
type RangeSpecInterface interface {
	// Check if this [`RangeSpec`] selects all chunks in the blob
	IsAll() bool
	// Checks if this [`RangeSpec`] does not select any chunks in the blob
	IsEmpty() bool
}

// A chunk range specification as a sequence of chunk offsets
type RangeSpec struct {
	ffiObject FfiObject
}

// Check if this [`RangeSpec`] selects all chunks in the blob
func (_self *RangeSpec) IsAll() bool {
	_pointer := _self.ffiObject.incrementPointer("*RangeSpec")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_iroh_ffi_fn_method_rangespec_is_all(
			_pointer, _uniffiStatus)
	}))
}

// Checks if this [`RangeSpec`] does not select any chunks in the blob
func (_self *RangeSpec) IsEmpty() bool {
	_pointer := _self.ffiObject.incrementPointer("*RangeSpec")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_iroh_ffi_fn_method_rangespec_is_empty(
			_pointer, _uniffiStatus)
	}))
}
func (object *RangeSpec) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterRangeSpec struct{}

var FfiConverterRangeSpecINSTANCE = FfiConverterRangeSpec{}

func (c FfiConverterRangeSpec) Lift(pointer unsafe.Pointer) *RangeSpec {
	result := &RangeSpec{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_rangespec(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_rangespec(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*RangeSpec).Destroy)
	return result
}

func (c FfiConverterRangeSpec) Read(reader io.Reader) *RangeSpec {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterRangeSpec) Lower(value *RangeSpec) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*RangeSpec")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterRangeSpec) Write(writer io.Writer, value *RangeSpec) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerRangeSpec struct{}

func (_ FfiDestroyerRangeSpec) Destroy(value *RangeSpec) {
	value.Destroy()
}

// Defines the way to read bytes.
type ReadAtLenInterface interface {
}

// Defines the way to read bytes.
type ReadAtLen struct {
	ffiObject FfiObject
}

func ReadAtLenAll() *ReadAtLen {
	return FfiConverterReadAtLenINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_readatlen_all(_uniffiStatus)
	}))
}

func ReadAtLenAtMost(size uint64) *ReadAtLen {
	return FfiConverterReadAtLenINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_readatlen_at_most(FfiConverterUint64INSTANCE.Lower(size), _uniffiStatus)
	}))
}

func ReadAtLenExact(size uint64) *ReadAtLen {
	return FfiConverterReadAtLenINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_readatlen_exact(FfiConverterUint64INSTANCE.Lower(size), _uniffiStatus)
	}))
}

func (object *ReadAtLen) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterReadAtLen struct{}

var FfiConverterReadAtLenINSTANCE = FfiConverterReadAtLen{}

func (c FfiConverterReadAtLen) Lift(pointer unsafe.Pointer) *ReadAtLen {
	result := &ReadAtLen{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_readatlen(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_readatlen(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*ReadAtLen).Destroy)
	return result
}

func (c FfiConverterReadAtLen) Read(reader io.Reader) *ReadAtLen {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterReadAtLen) Lower(value *ReadAtLen) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*ReadAtLen")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterReadAtLen) Write(writer io.Writer, value *ReadAtLen) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerReadAtLen struct{}

func (_ FfiDestroyerReadAtLen) Destroy(value *ReadAtLen) {
	value.Destroy()
}

type RecvStreamInterface interface {
	Id() string
	Read(sizeLimit uint32) ([]byte, *IrohError)
	ReadExact(size uint32) ([]byte, *IrohError)
	ReadToEnd(sizeLimit uint32) ([]byte, *IrohError)
	ReceivedReset() (*uint64, *IrohError)
	Stop(errorCode uint64) *IrohError
}
type RecvStream struct {
	ffiObject FfiObject
}

func (_self *RecvStream) Id() string {
	_pointer := _self.ffiObject.incrementPointer("*RecvStream")
	defer _self.ffiObject.decrementPointer()
	res, _ := uniffiRustCallAsync[struct{}](
		nil,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) string {
			return FfiConverterStringINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_recvstream_id(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res
}

func (_self *RecvStream) Read(sizeLimit uint32) ([]byte, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*RecvStream")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []byte {
			return FfiConverterBytesINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_recvstream_read(
			_pointer, FfiConverterUint32INSTANCE.Lower(sizeLimit)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

func (_self *RecvStream) ReadExact(size uint32) ([]byte, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*RecvStream")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []byte {
			return FfiConverterBytesINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_recvstream_read_exact(
			_pointer, FfiConverterUint32INSTANCE.Lower(size)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

func (_self *RecvStream) ReadToEnd(sizeLimit uint32) ([]byte, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*RecvStream")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []byte {
			return FfiConverterBytesINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_recvstream_read_to_end(
			_pointer, FfiConverterUint32INSTANCE.Lower(sizeLimit)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

func (_self *RecvStream) ReceivedReset() (*uint64, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*RecvStream")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *uint64 {
			return FfiConverterOptionalUint64INSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_recvstream_received_reset(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

func (_self *RecvStream) Stop(errorCode uint64) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*RecvStream")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_recvstream_stop(
			_pointer, FfiConverterUint64INSTANCE.Lower(errorCode)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}
func (object *RecvStream) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterRecvStream struct{}

var FfiConverterRecvStreamINSTANCE = FfiConverterRecvStream{}

func (c FfiConverterRecvStream) Lift(pointer unsafe.Pointer) *RecvStream {
	result := &RecvStream{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_recvstream(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_recvstream(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*RecvStream).Destroy)
	return result
}

func (c FfiConverterRecvStream) Read(reader io.Reader) *RecvStream {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterRecvStream) Lower(value *RecvStream) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*RecvStream")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterRecvStream) Write(writer io.Writer, value *RecvStream) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerRecvStream struct{}

func (_ FfiDestroyerRecvStream) Destroy(value *RecvStream) {
	value.Destroy()
}

type SendStreamInterface interface {
	Finish() *IrohError
	Id() string
	Priority() (int32, *IrohError)
	Reset(errorCode uint64) *IrohError
	SetPriority(p int32) *IrohError
	Stopped() (*uint64, *IrohError)
	Write(buf []byte) (uint64, *IrohError)
	WriteAll(buf []byte) *IrohError
}
type SendStream struct {
	ffiObject FfiObject
}

func (_self *SendStream) Finish() *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*SendStream")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_sendstream_finish(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

func (_self *SendStream) Id() string {
	_pointer := _self.ffiObject.incrementPointer("*SendStream")
	defer _self.ffiObject.decrementPointer()
	res, _ := uniffiRustCallAsync[struct{}](
		nil,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) string {
			return FfiConverterStringINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_sendstream_id(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res
}

func (_self *SendStream) Priority() (int32, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*SendStream")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) C.int32_t {
			res := C.ffi_iroh_ffi_rust_future_complete_i32(handle, status)
			return res
		},
		// liftFn
		func(ffi C.int32_t) int32 {
			return FfiConverterInt32INSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_sendstream_priority(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_i32(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_i32(handle)
		},
	)

	return res, err
}

func (_self *SendStream) Reset(errorCode uint64) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*SendStream")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_sendstream_reset(
			_pointer, FfiConverterUint64INSTANCE.Lower(errorCode)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

func (_self *SendStream) SetPriority(p int32) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*SendStream")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_sendstream_set_priority(
			_pointer, FfiConverterInt32INSTANCE.Lower(p)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

func (_self *SendStream) Stopped() (*uint64, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*SendStream")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *uint64 {
			return FfiConverterOptionalUint64INSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_sendstream_stopped(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}

func (_self *SendStream) Write(buf []byte) (uint64, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*SendStream")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) C.uint64_t {
			res := C.ffi_iroh_ffi_rust_future_complete_u64(handle, status)
			return res
		},
		// liftFn
		func(ffi C.uint64_t) uint64 {
			return FfiConverterUint64INSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_sendstream_write(
			_pointer, FfiConverterBytesINSTANCE.Lower(buf)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_u64(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_u64(handle)
		},
	)

	return res, err
}

func (_self *SendStream) WriteAll(buf []byte) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*SendStream")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_sendstream_write_all(
			_pointer, FfiConverterBytesINSTANCE.Lower(buf)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}
func (object *SendStream) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterSendStream struct{}

var FfiConverterSendStreamINSTANCE = FfiConverterSendStream{}

func (c FfiConverterSendStream) Lift(pointer unsafe.Pointer) *SendStream {
	result := &SendStream{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_sendstream(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_sendstream(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*SendStream).Destroy)
	return result
}

func (c FfiConverterSendStream) Read(reader io.Reader) *SendStream {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterSendStream) Lower(value *SendStream) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*SendStream")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterSendStream) Write(writer io.Writer, value *SendStream) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerSendStream struct{}

func (_ FfiDestroyerSendStream) Destroy(value *SendStream) {
	value.Destroy()
}

// Gossip sender
type SenderInterface interface {
	// Broadcast a message to all nodes in the swarm
	Broadcast(msg []byte) *IrohError
	// Broadcast a message to all direct neighbors.
	BroadcastNeighbors(msg []byte) *IrohError
	// Closes the subscription, it is an error to use it afterwards
	Cancel() *IrohError
}

// Gossip sender
type Sender struct {
	ffiObject FfiObject
}

// Broadcast a message to all nodes in the swarm
func (_self *Sender) Broadcast(msg []byte) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Sender")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_sender_broadcast(
			_pointer, FfiConverterBytesINSTANCE.Lower(msg)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Broadcast a message to all direct neighbors.
func (_self *Sender) BroadcastNeighbors(msg []byte) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Sender")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_sender_broadcast_neighbors(
			_pointer, FfiConverterBytesINSTANCE.Lower(msg)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// Closes the subscription, it is an error to use it afterwards
func (_self *Sender) Cancel() *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Sender")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_sender_cancel(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}
func (object *Sender) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterSender struct{}

var FfiConverterSenderINSTANCE = FfiConverterSender{}

func (c FfiConverterSender) Lift(pointer unsafe.Pointer) *Sender {
	result := &Sender{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_sender(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_sender(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Sender).Destroy)
	return result
}

func (c FfiConverterSender) Read(reader io.Reader) *Sender {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterSender) Lower(value *Sender) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Sender")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterSender) Write(writer io.Writer, value *Sender) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerSender struct{}

func (_ FfiDestroyerSender) Destroy(value *Sender) {
	value.Destroy()
}

// An option for commands that allow setting a Tag
type SetTagOptionInterface interface {
}

// An option for commands that allow setting a Tag
type SetTagOption struct {
	ffiObject FfiObject
}

// Indicate you want an automatically generated tag
func SetTagOptionAuto() *SetTagOption {
	return FfiConverterSetTagOptionINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_settagoption_auto(_uniffiStatus)
	}))
}

// Indicate you want a named tag
func SetTagOptionNamed(tag []byte) *SetTagOption {
	return FfiConverterSetTagOptionINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_settagoption_named(FfiConverterBytesINSTANCE.Lower(tag), _uniffiStatus)
	}))
}

func (object *SetTagOption) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterSetTagOption struct{}

var FfiConverterSetTagOptionINSTANCE = FfiConverterSetTagOption{}

func (c FfiConverterSetTagOption) Lift(pointer unsafe.Pointer) *SetTagOption {
	result := &SetTagOption{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_settagoption(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_settagoption(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*SetTagOption).Destroy)
	return result
}

func (c FfiConverterSetTagOption) Read(reader io.Reader) *SetTagOption {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterSetTagOption) Lower(value *SetTagOption) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*SetTagOption")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterSetTagOption) Write(writer io.Writer, value *SetTagOption) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerSetTagOption struct{}

func (_ FfiDestroyerSetTagOption) Destroy(value *SetTagOption) {
	value.Destroy()
}

// The `progress` method will be called for each `SubscribeProgress` event that is
// emitted during a `node.doc_subscribe`. Use the `SubscribeProgress.type()`
// method to check the `LiveEvent`
type SubscribeCallback interface {
	Event(event *LiveEvent) *CallbackError
}

// The `progress` method will be called for each `SubscribeProgress` event that is
// emitted during a `node.doc_subscribe`. Use the `SubscribeProgress.type()`
// method to check the `LiveEvent`
type SubscribeCallbackImpl struct {
	ffiObject FfiObject
}

func (_self *SubscribeCallbackImpl) Event(event *LiveEvent) *CallbackError {
	_pointer := _self.ffiObject.incrementPointer("SubscribeCallback")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[CallbackError](
		FfiConverterCallbackErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_subscribecallback_event(
			_pointer, FfiConverterLiveEventINSTANCE.Lower(event)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}
func (object *SubscribeCallbackImpl) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterSubscribeCallback struct {
	handleMap *concurrentHandleMap[SubscribeCallback]
}

var FfiConverterSubscribeCallbackINSTANCE = FfiConverterSubscribeCallback{
	handleMap: newConcurrentHandleMap[SubscribeCallback](),
}

func (c FfiConverterSubscribeCallback) Lift(pointer unsafe.Pointer) SubscribeCallback {
	result := &SubscribeCallbackImpl{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_subscribecallback(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_subscribecallback(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*SubscribeCallbackImpl).Destroy)
	return result
}

func (c FfiConverterSubscribeCallback) Read(reader io.Reader) SubscribeCallback {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterSubscribeCallback) Lower(value SubscribeCallback) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := unsafe.Pointer(uintptr(c.handleMap.insert(value)))
	return pointer

}

func (c FfiConverterSubscribeCallback) Write(writer io.Writer, value SubscribeCallback) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerSubscribeCallback struct{}

func (_ FfiDestroyerSubscribeCallback) Destroy(value SubscribeCallback) {
	if val, ok := value.(*SubscribeCallbackImpl); ok {
		val.Destroy()
	} else {
		panic("Expected *SubscribeCallbackImpl")
	}
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceSubscribeCallbackMethod0
func iroh_ffi_cgo_dispatchCallbackInterfaceSubscribeCallbackMethod0(uniffiHandle C.uint64_t, event unsafe.Pointer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterSubscribeCallbackINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.iroh_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.Event(
				FfiConverterLiveEventINSTANCE.Lift(event),
			)

		if err != nil {
			// The only way to bypass an unexpected error is to bypass pointer to an empty
			// instance of the error
			if err.err == nil {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
				return
			}

			*callStatus = C.RustCallStatus{
				code:     C.int8_t(uniffiCallbackResultError),
				errorBuf: FfiConverterCallbackErrorINSTANCE.Lower(err),
			}
			return
		}

	}()
}

var UniffiVTableCallbackInterfaceSubscribeCallbackINSTANCE = C.UniffiVTableCallbackInterfaceSubscribeCallback{
	event: (C.UniffiCallbackInterfaceSubscribeCallbackMethod0)(C.iroh_ffi_cgo_dispatchCallbackInterfaceSubscribeCallbackMethod0),

	uniffiFree: (C.UniffiCallbackInterfaceFree)(C.iroh_ffi_cgo_dispatchCallbackInterfaceSubscribeCallbackFree),
}

//export iroh_ffi_cgo_dispatchCallbackInterfaceSubscribeCallbackFree
func iroh_ffi_cgo_dispatchCallbackInterfaceSubscribeCallbackFree(handle C.uint64_t) {
	FfiConverterSubscribeCallbackINSTANCE.handleMap.remove(uint64(handle))
}

func (c FfiConverterSubscribeCallback) register() {
	C.uniffi_iroh_ffi_fn_init_callback_vtable_subscribecallback(&UniffiVTableCallbackInterfaceSubscribeCallbackINSTANCE)
}

// Iroh tags client.
type TagsInterface interface {
	// Delete a tag
	Delete(name []byte) *IrohError
	// List all tags
	//
	// Note: this allocates for each `ListTagsResponse`, if you have many `Tags`s this may be a prohibitively large list.
	// Please file an [issue](https://github.com/n0-computer/iroh-ffi/issues/new) if you run into this issue
	List() ([]TagInfo, *IrohError)
}

// Iroh tags client.
type Tags struct {
	ffiObject FfiObject
}

// Delete a tag
func (_self *Tags) Delete(name []byte) *IrohError {
	_pointer := _self.ffiObject.incrementPointer("*Tags")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_iroh_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_iroh_ffi_fn_method_tags_delete(
			_pointer, FfiConverterBytesINSTANCE.Lower(name)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_void(handle)
		},
	)

	return err
}

// List all tags
//
// Note: this allocates for each `ListTagsResponse`, if you have many `Tags`s this may be a prohibitively large list.
// Please file an [issue](https://github.com/n0-computer/iroh-ffi/issues/new) if you run into this issue
func (_self *Tags) List() ([]TagInfo, *IrohError) {
	_pointer := _self.ffiObject.incrementPointer("*Tags")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[IrohError](
		FfiConverterIrohErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_iroh_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []TagInfo {
			return FfiConverterSequenceTagInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_iroh_ffi_fn_method_tags_list(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_iroh_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res, err
}
func (object *Tags) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterTags struct{}

var FfiConverterTagsINSTANCE = FfiConverterTags{}

func (c FfiConverterTags) Lift(pointer unsafe.Pointer) *Tags {
	result := &Tags{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_tags(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_tags(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Tags).Destroy)
	return result
}

func (c FfiConverterTags) Read(reader io.Reader) *Tags {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterTags) Lower(value *Tags) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Tags")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterTags) Write(writer io.Writer, value *Tags) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerTags struct{}

func (_ FfiDestroyerTags) Destroy(value *Tags) {
	value.Destroy()
}

// Whether to wrap the added data in a collection.
type WrapOptionInterface interface {
}

// Whether to wrap the added data in a collection.
type WrapOption struct {
	ffiObject FfiObject
}

// Indicate you do not wrap the file or directory.
func WrapOptionNoWrap() *WrapOption {
	return FfiConverterWrapOptionINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_wrapoption_no_wrap(_uniffiStatus)
	}))
}

// Indicate you want to wrap the file or directory in a colletion, with an optional name
func WrapOptionWrap(name *string) *WrapOption {
	return FfiConverterWrapOptionINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_iroh_ffi_fn_constructor_wrapoption_wrap(FfiConverterOptionalStringINSTANCE.Lower(name), _uniffiStatus)
	}))
}

func (object *WrapOption) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterWrapOption struct{}

var FfiConverterWrapOptionINSTANCE = FfiConverterWrapOption{}

func (c FfiConverterWrapOption) Lift(pointer unsafe.Pointer) *WrapOption {
	result := &WrapOption{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_iroh_ffi_fn_clone_wrapoption(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_iroh_ffi_fn_free_wrapoption(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*WrapOption).Destroy)
	return result
}

func (c FfiConverterWrapOption) Read(reader io.Reader) *WrapOption {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterWrapOption) Lower(value *WrapOption) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*WrapOption")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterWrapOption) Write(writer io.Writer, value *WrapOption) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerWrapOption struct{}

func (_ FfiDestroyerWrapOption) Destroy(value *WrapOption) {
	value.Destroy()
}

// An AddProgress event indicating we got an error and need to abort
type AddProgressAbort struct {
	Error string
}

func (r *AddProgressAbort) Destroy() {
	FfiDestroyerString{}.Destroy(r.Error)
}

type FfiConverterAddProgressAbort struct{}

var FfiConverterAddProgressAbortINSTANCE = FfiConverterAddProgressAbort{}

func (c FfiConverterAddProgressAbort) Lift(rb RustBufferI) AddProgressAbort {
	return LiftFromRustBuffer[AddProgressAbort](c, rb)
}

func (c FfiConverterAddProgressAbort) Read(reader io.Reader) AddProgressAbort {
	return AddProgressAbort{
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterAddProgressAbort) Lower(value AddProgressAbort) C.RustBuffer {
	return LowerIntoRustBuffer[AddProgressAbort](c, value)
}

func (c FfiConverterAddProgressAbort) Write(writer io.Writer, value AddProgressAbort) {
	FfiConverterStringINSTANCE.Write(writer, value.Error)
}

type FfiDestroyerAddProgressAbort struct{}

func (_ FfiDestroyerAddProgressAbort) Destroy(value AddProgressAbort) {
	value.Destroy()
}

// An AddProgress event indicating we are done with the the whole operation
type AddProgressAllDone struct {
	// The hash of the created data.
	Hash *Hash
	// The format of the added data.
	Format BlobFormat
	// The tag of the added data.
	Tag []byte
}

func (r *AddProgressAllDone) Destroy() {
	FfiDestroyerHash{}.Destroy(r.Hash)
	FfiDestroyerBlobFormat{}.Destroy(r.Format)
	FfiDestroyerBytes{}.Destroy(r.Tag)
}

type FfiConverterAddProgressAllDone struct{}

var FfiConverterAddProgressAllDoneINSTANCE = FfiConverterAddProgressAllDone{}

func (c FfiConverterAddProgressAllDone) Lift(rb RustBufferI) AddProgressAllDone {
	return LiftFromRustBuffer[AddProgressAllDone](c, rb)
}

func (c FfiConverterAddProgressAllDone) Read(reader io.Reader) AddProgressAllDone {
	return AddProgressAllDone{
		FfiConverterHashINSTANCE.Read(reader),
		FfiConverterBlobFormatINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterAddProgressAllDone) Lower(value AddProgressAllDone) C.RustBuffer {
	return LowerIntoRustBuffer[AddProgressAllDone](c, value)
}

func (c FfiConverterAddProgressAllDone) Write(writer io.Writer, value AddProgressAllDone) {
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
	FfiConverterBlobFormatINSTANCE.Write(writer, value.Format)
	FfiConverterBytesINSTANCE.Write(writer, value.Tag)
}

type FfiDestroyerAddProgressAllDone struct{}

func (_ FfiDestroyerAddProgressAllDone) Destroy(value AddProgressAllDone) {
	value.Destroy()
}

// An AddProgress event indicated we are done with `id` and now have a hash `hash`
type AddProgressDone struct {
	// The unique id of the entry.
	Id uint64
	// The hash of the entry.
	Hash *Hash
}

func (r *AddProgressDone) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Id)
	FfiDestroyerHash{}.Destroy(r.Hash)
}

type FfiConverterAddProgressDone struct{}

var FfiConverterAddProgressDoneINSTANCE = FfiConverterAddProgressDone{}

func (c FfiConverterAddProgressDone) Lift(rb RustBufferI) AddProgressDone {
	return LiftFromRustBuffer[AddProgressDone](c, rb)
}

func (c FfiConverterAddProgressDone) Read(reader io.Reader) AddProgressDone {
	return AddProgressDone{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterHashINSTANCE.Read(reader),
	}
}

func (c FfiConverterAddProgressDone) Lower(value AddProgressDone) C.RustBuffer {
	return LowerIntoRustBuffer[AddProgressDone](c, value)
}

func (c FfiConverterAddProgressDone) Write(writer io.Writer, value AddProgressDone) {
	FfiConverterUint64INSTANCE.Write(writer, value.Id)
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
}

type FfiDestroyerAddProgressDone struct{}

func (_ FfiDestroyerAddProgressDone) Destroy(value AddProgressDone) {
	value.Destroy()
}

// An AddProgress event indicating an item was found with name `name`, that can be referred to by `id`
type AddProgressFound struct {
	// A new unique id for this entry.
	Id uint64
	// The name of the entry.
	Name string
	// The size of the entry in bytes.
	Size uint64
}

func (r *AddProgressFound) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Id)
	FfiDestroyerString{}.Destroy(r.Name)
	FfiDestroyerUint64{}.Destroy(r.Size)
}

type FfiConverterAddProgressFound struct{}

var FfiConverterAddProgressFoundINSTANCE = FfiConverterAddProgressFound{}

func (c FfiConverterAddProgressFound) Lift(rb RustBufferI) AddProgressFound {
	return LiftFromRustBuffer[AddProgressFound](c, rb)
}

func (c FfiConverterAddProgressFound) Read(reader io.Reader) AddProgressFound {
	return AddProgressFound{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterAddProgressFound) Lower(value AddProgressFound) C.RustBuffer {
	return LowerIntoRustBuffer[AddProgressFound](c, value)
}

func (c FfiConverterAddProgressFound) Write(writer io.Writer, value AddProgressFound) {
	FfiConverterUint64INSTANCE.Write(writer, value.Id)
	FfiConverterStringINSTANCE.Write(writer, value.Name)
	FfiConverterUint64INSTANCE.Write(writer, value.Size)
}

type FfiDestroyerAddProgressFound struct{}

func (_ FfiDestroyerAddProgressFound) Destroy(value AddProgressFound) {
	value.Destroy()
}

// An AddProgress event indicating we got progress ingesting item `id`.
type AddProgressProgress struct {
	// The unique id of the entry.
	Id uint64
	// The offset of the progress, in bytes.
	Offset uint64
}

func (r *AddProgressProgress) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Id)
	FfiDestroyerUint64{}.Destroy(r.Offset)
}

type FfiConverterAddProgressProgress struct{}

var FfiConverterAddProgressProgressINSTANCE = FfiConverterAddProgressProgress{}

func (c FfiConverterAddProgressProgress) Lift(rb RustBufferI) AddProgressProgress {
	return LiftFromRustBuffer[AddProgressProgress](c, rb)
}

func (c FfiConverterAddProgressProgress) Read(reader io.Reader) AddProgressProgress {
	return AddProgressProgress{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterAddProgressProgress) Lower(value AddProgressProgress) C.RustBuffer {
	return LowerIntoRustBuffer[AddProgressProgress](c, value)
}

func (c FfiConverterAddProgressProgress) Write(writer io.Writer, value AddProgressProgress) {
	FfiConverterUint64INSTANCE.Write(writer, value.Id)
	FfiConverterUint64INSTANCE.Write(writer, value.Offset)
}

type FfiDestroyerAddProgressProgress struct{}

func (_ FfiDestroyerAddProgressProgress) Destroy(value AddProgressProgress) {
	value.Destroy()
}

// Outcome of a blob add operation.
type BlobAddOutcome struct {
	// The hash of the blob
	Hash *Hash
	// The format the blob
	Format BlobFormat
	// The size of the blob
	Size uint64
	// The tag of the blob
	Tag []byte
}

func (r *BlobAddOutcome) Destroy() {
	FfiDestroyerHash{}.Destroy(r.Hash)
	FfiDestroyerBlobFormat{}.Destroy(r.Format)
	FfiDestroyerUint64{}.Destroy(r.Size)
	FfiDestroyerBytes{}.Destroy(r.Tag)
}

type FfiConverterBlobAddOutcome struct{}

var FfiConverterBlobAddOutcomeINSTANCE = FfiConverterBlobAddOutcome{}

func (c FfiConverterBlobAddOutcome) Lift(rb RustBufferI) BlobAddOutcome {
	return LiftFromRustBuffer[BlobAddOutcome](c, rb)
}

func (c FfiConverterBlobAddOutcome) Read(reader io.Reader) BlobAddOutcome {
	return BlobAddOutcome{
		FfiConverterHashINSTANCE.Read(reader),
		FfiConverterBlobFormatINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterBlobAddOutcome) Lower(value BlobAddOutcome) C.RustBuffer {
	return LowerIntoRustBuffer[BlobAddOutcome](c, value)
}

func (c FfiConverterBlobAddOutcome) Write(writer io.Writer, value BlobAddOutcome) {
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
	FfiConverterBlobFormatINSTANCE.Write(writer, value.Format)
	FfiConverterUint64INSTANCE.Write(writer, value.Size)
	FfiConverterBytesINSTANCE.Write(writer, value.Tag)
}

type FfiDestroyerBlobAddOutcome struct{}

func (_ FfiDestroyerBlobAddOutcome) Destroy(value BlobAddOutcome) {
	value.Destroy()
}

// A response to a list blobs request
type BlobInfo struct {
	// Location of the blob
	Path string
	// The hash of the blob
	Hash *Hash
	// The size of the blob
	Size uint64
}

func (r *BlobInfo) Destroy() {
	FfiDestroyerString{}.Destroy(r.Path)
	FfiDestroyerHash{}.Destroy(r.Hash)
	FfiDestroyerUint64{}.Destroy(r.Size)
}

type FfiConverterBlobInfo struct{}

var FfiConverterBlobInfoINSTANCE = FfiConverterBlobInfo{}

func (c FfiConverterBlobInfo) Lift(rb RustBufferI) BlobInfo {
	return LiftFromRustBuffer[BlobInfo](c, rb)
}

func (c FfiConverterBlobInfo) Read(reader io.Reader) BlobInfo {
	return BlobInfo{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterHashINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterBlobInfo) Lower(value BlobInfo) C.RustBuffer {
	return LowerIntoRustBuffer[BlobInfo](c, value)
}

func (c FfiConverterBlobInfo) Write(writer io.Writer, value BlobInfo) {
	FfiConverterStringINSTANCE.Write(writer, value.Path)
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
	FfiConverterUint64INSTANCE.Write(writer, value.Size)
}

type FfiDestroyerBlobInfo struct{}

func (_ FfiDestroyerBlobInfo) Destroy(value BlobInfo) {
	value.Destroy()
}

// A new client connected to the node.
type ClientConnected struct {
	// An unique connection id.
	ConnectionId uint64
}

func (r *ClientConnected) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.ConnectionId)
}

type FfiConverterClientConnected struct{}

var FfiConverterClientConnectedINSTANCE = FfiConverterClientConnected{}

func (c FfiConverterClientConnected) Lift(rb RustBufferI) ClientConnected {
	return LiftFromRustBuffer[ClientConnected](c, rb)
}

func (c FfiConverterClientConnected) Read(reader io.Reader) ClientConnected {
	return ClientConnected{
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterClientConnected) Lower(value ClientConnected) C.RustBuffer {
	return LowerIntoRustBuffer[ClientConnected](c, value)
}

func (c FfiConverterClientConnected) Write(writer io.Writer, value ClientConnected) {
	FfiConverterUint64INSTANCE.Write(writer, value.ConnectionId)
}

type FfiDestroyerClientConnected struct{}

func (_ FfiDestroyerClientConnected) Destroy(value ClientConnected) {
	value.Destroy()
}

// A response to a list collections request
type CollectionInfo struct {
	// Tag of the collection
	Tag []byte
	// Hash of the collection
	Hash *Hash
	// Number of children in the collection
	//
	// This is an optional field, because the data is not always available.
	TotalBlobsCount *uint64
	// Total size of the raw data referred to by all links
	//
	// This is an optional field, because the data is not always available.
	TotalBlobsSize *uint64
}

func (r *CollectionInfo) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.Tag)
	FfiDestroyerHash{}.Destroy(r.Hash)
	FfiDestroyerOptionalUint64{}.Destroy(r.TotalBlobsCount)
	FfiDestroyerOptionalUint64{}.Destroy(r.TotalBlobsSize)
}

type FfiConverterCollectionInfo struct{}

var FfiConverterCollectionInfoINSTANCE = FfiConverterCollectionInfo{}

func (c FfiConverterCollectionInfo) Lift(rb RustBufferI) CollectionInfo {
	return LiftFromRustBuffer[CollectionInfo](c, rb)
}

func (c FfiConverterCollectionInfo) Read(reader io.Reader) CollectionInfo {
	return CollectionInfo{
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterHashINSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterCollectionInfo) Lower(value CollectionInfo) C.RustBuffer {
	return LowerIntoRustBuffer[CollectionInfo](c, value)
}

func (c FfiConverterCollectionInfo) Write(writer io.Writer, value CollectionInfo) {
	FfiConverterBytesINSTANCE.Write(writer, value.Tag)
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.TotalBlobsCount)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.TotalBlobsSize)
}

type FfiDestroyerCollectionInfo struct{}

func (_ FfiDestroyerCollectionInfo) Destroy(value CollectionInfo) {
	value.Destroy()
}

// The socket address and url of the mixed connection
type ConnectionTypeMixed struct {
	// Address of the node
	Addr string
	// Url of the relay node to which the node is connected
	RelayUrl string
}

func (r *ConnectionTypeMixed) Destroy() {
	FfiDestroyerString{}.Destroy(r.Addr)
	FfiDestroyerString{}.Destroy(r.RelayUrl)
}

type FfiConverterConnectionTypeMixed struct{}

var FfiConverterConnectionTypeMixedINSTANCE = FfiConverterConnectionTypeMixed{}

func (c FfiConverterConnectionTypeMixed) Lift(rb RustBufferI) ConnectionTypeMixed {
	return LiftFromRustBuffer[ConnectionTypeMixed](c, rb)
}

func (c FfiConverterConnectionTypeMixed) Read(reader io.Reader) ConnectionTypeMixed {
	return ConnectionTypeMixed{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterConnectionTypeMixed) Lower(value ConnectionTypeMixed) C.RustBuffer {
	return LowerIntoRustBuffer[ConnectionTypeMixed](c, value)
}

func (c FfiConverterConnectionTypeMixed) Write(writer io.Writer, value ConnectionTypeMixed) {
	FfiConverterStringINSTANCE.Write(writer, value.Addr)
	FfiConverterStringINSTANCE.Write(writer, value.RelayUrl)
}

type FfiDestroyerConnectionTypeMixed struct{}

func (_ FfiDestroyerConnectionTypeMixed) Destroy(value ConnectionTypeMixed) {
	value.Destroy()
}

// Stats counter
type CounterStats struct {
	// The counter value
	Value uint32
	// The counter description
	Description string
}

func (r *CounterStats) Destroy() {
	FfiDestroyerUint32{}.Destroy(r.Value)
	FfiDestroyerString{}.Destroy(r.Description)
}

type FfiConverterCounterStats struct{}

var FfiConverterCounterStatsINSTANCE = FfiConverterCounterStats{}

func (c FfiConverterCounterStats) Lift(rb RustBufferI) CounterStats {
	return LiftFromRustBuffer[CounterStats](c, rb)
}

func (c FfiConverterCounterStats) Read(reader io.Reader) CounterStats {
	return CounterStats{
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterCounterStats) Lower(value CounterStats) C.RustBuffer {
	return LowerIntoRustBuffer[CounterStats](c, value)
}

func (c FfiConverterCounterStats) Write(writer io.Writer, value CounterStats) {
	FfiConverterUint32INSTANCE.Write(writer, value.Value)
	FfiConverterStringINSTANCE.Write(writer, value.Description)
}

type FfiDestroyerCounterStats struct{}

func (_ FfiDestroyerCounterStats) Destroy(value CounterStats) {
	value.Destroy()
}

// A DocExportProgress event indicating we got an error and need to abort
type DocExportProgressAbort struct {
	// The error message
	Error string
}

func (r *DocExportProgressAbort) Destroy() {
	FfiDestroyerString{}.Destroy(r.Error)
}

type FfiConverterDocExportProgressAbort struct{}

var FfiConverterDocExportProgressAbortINSTANCE = FfiConverterDocExportProgressAbort{}

func (c FfiConverterDocExportProgressAbort) Lift(rb RustBufferI) DocExportProgressAbort {
	return LiftFromRustBuffer[DocExportProgressAbort](c, rb)
}

func (c FfiConverterDocExportProgressAbort) Read(reader io.Reader) DocExportProgressAbort {
	return DocExportProgressAbort{
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterDocExportProgressAbort) Lower(value DocExportProgressAbort) C.RustBuffer {
	return LowerIntoRustBuffer[DocExportProgressAbort](c, value)
}

func (c FfiConverterDocExportProgressAbort) Write(writer io.Writer, value DocExportProgressAbort) {
	FfiConverterStringINSTANCE.Write(writer, value.Error)
}

type FfiDestroyerDocExportProgressAbort struct{}

func (_ FfiDestroyerDocExportProgressAbort) Destroy(value DocExportProgressAbort) {
	value.Destroy()
}

// A DocExportProgress event indicating a single blob wit `id` is done
type DocExportProgressDone struct {
	// The unique id of the entry.
	Id uint64
}

func (r *DocExportProgressDone) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Id)
}

type FfiConverterDocExportProgressDone struct{}

var FfiConverterDocExportProgressDoneINSTANCE = FfiConverterDocExportProgressDone{}

func (c FfiConverterDocExportProgressDone) Lift(rb RustBufferI) DocExportProgressDone {
	return LiftFromRustBuffer[DocExportProgressDone](c, rb)
}

func (c FfiConverterDocExportProgressDone) Read(reader io.Reader) DocExportProgressDone {
	return DocExportProgressDone{
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterDocExportProgressDone) Lower(value DocExportProgressDone) C.RustBuffer {
	return LowerIntoRustBuffer[DocExportProgressDone](c, value)
}

func (c FfiConverterDocExportProgressDone) Write(writer io.Writer, value DocExportProgressDone) {
	FfiConverterUint64INSTANCE.Write(writer, value.Id)
}

type FfiDestroyerDocExportProgressDone struct{}

func (_ FfiDestroyerDocExportProgressDone) Destroy(value DocExportProgressDone) {
	value.Destroy()
}

// A DocExportProgress event indicating a file was found with name `name`, from now on referred to via `id`
type DocExportProgressFound struct {
	// A new unique id for this entry.
	Id uint64
	// The hash of the entry.
	Hash *Hash
	// The size of the entry in bytes.
	Size uint64
	// The path where we are writing the entry
	Outpath string
}

func (r *DocExportProgressFound) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Id)
	FfiDestroyerHash{}.Destroy(r.Hash)
	FfiDestroyerUint64{}.Destroy(r.Size)
	FfiDestroyerString{}.Destroy(r.Outpath)
}

type FfiConverterDocExportProgressFound struct{}

var FfiConverterDocExportProgressFoundINSTANCE = FfiConverterDocExportProgressFound{}

func (c FfiConverterDocExportProgressFound) Lift(rb RustBufferI) DocExportProgressFound {
	return LiftFromRustBuffer[DocExportProgressFound](c, rb)
}

func (c FfiConverterDocExportProgressFound) Read(reader io.Reader) DocExportProgressFound {
	return DocExportProgressFound{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterHashINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterDocExportProgressFound) Lower(value DocExportProgressFound) C.RustBuffer {
	return LowerIntoRustBuffer[DocExportProgressFound](c, value)
}

func (c FfiConverterDocExportProgressFound) Write(writer io.Writer, value DocExportProgressFound) {
	FfiConverterUint64INSTANCE.Write(writer, value.Id)
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
	FfiConverterUint64INSTANCE.Write(writer, value.Size)
	FfiConverterStringINSTANCE.Write(writer, value.Outpath)
}

type FfiDestroyerDocExportProgressFound struct{}

func (_ FfiDestroyerDocExportProgressFound) Destroy(value DocExportProgressFound) {
	value.Destroy()
}

// A DocExportProgress event indicating we've made progress exporting item `id`.
type DocExportProgressProgress struct {
	// The unique id of the entry.
	Id uint64
	// The offset of the progress, in bytes.
	Offset uint64
}

func (r *DocExportProgressProgress) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Id)
	FfiDestroyerUint64{}.Destroy(r.Offset)
}

type FfiConverterDocExportProgressProgress struct{}

var FfiConverterDocExportProgressProgressINSTANCE = FfiConverterDocExportProgressProgress{}

func (c FfiConverterDocExportProgressProgress) Lift(rb RustBufferI) DocExportProgressProgress {
	return LiftFromRustBuffer[DocExportProgressProgress](c, rb)
}

func (c FfiConverterDocExportProgressProgress) Read(reader io.Reader) DocExportProgressProgress {
	return DocExportProgressProgress{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterDocExportProgressProgress) Lower(value DocExportProgressProgress) C.RustBuffer {
	return LowerIntoRustBuffer[DocExportProgressProgress](c, value)
}

func (c FfiConverterDocExportProgressProgress) Write(writer io.Writer, value DocExportProgressProgress) {
	FfiConverterUint64INSTANCE.Write(writer, value.Id)
	FfiConverterUint64INSTANCE.Write(writer, value.Offset)
}

type FfiDestroyerDocExportProgressProgress struct{}

func (_ FfiDestroyerDocExportProgressProgress) Destroy(value DocExportProgressProgress) {
	value.Destroy()
}

// A DocImportProgress event indicating we got an error and need to abort
type DocImportProgressAbort struct {
	// The error message
	Error string
}

func (r *DocImportProgressAbort) Destroy() {
	FfiDestroyerString{}.Destroy(r.Error)
}

type FfiConverterDocImportProgressAbort struct{}

var FfiConverterDocImportProgressAbortINSTANCE = FfiConverterDocImportProgressAbort{}

func (c FfiConverterDocImportProgressAbort) Lift(rb RustBufferI) DocImportProgressAbort {
	return LiftFromRustBuffer[DocImportProgressAbort](c, rb)
}

func (c FfiConverterDocImportProgressAbort) Read(reader io.Reader) DocImportProgressAbort {
	return DocImportProgressAbort{
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterDocImportProgressAbort) Lower(value DocImportProgressAbort) C.RustBuffer {
	return LowerIntoRustBuffer[DocImportProgressAbort](c, value)
}

func (c FfiConverterDocImportProgressAbort) Write(writer io.Writer, value DocImportProgressAbort) {
	FfiConverterStringINSTANCE.Write(writer, value.Error)
}

type FfiDestroyerDocImportProgressAbort struct{}

func (_ FfiDestroyerDocImportProgressAbort) Destroy(value DocImportProgressAbort) {
	value.Destroy()
}

// A DocImportProgress event indicating we are done setting the entry to the doc
type DocImportProgressAllDone struct {
	// The key of the entry
	Key []byte
}

func (r *DocImportProgressAllDone) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.Key)
}

type FfiConverterDocImportProgressAllDone struct{}

var FfiConverterDocImportProgressAllDoneINSTANCE = FfiConverterDocImportProgressAllDone{}

func (c FfiConverterDocImportProgressAllDone) Lift(rb RustBufferI) DocImportProgressAllDone {
	return LiftFromRustBuffer[DocImportProgressAllDone](c, rb)
}

func (c FfiConverterDocImportProgressAllDone) Read(reader io.Reader) DocImportProgressAllDone {
	return DocImportProgressAllDone{
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterDocImportProgressAllDone) Lower(value DocImportProgressAllDone) C.RustBuffer {
	return LowerIntoRustBuffer[DocImportProgressAllDone](c, value)
}

func (c FfiConverterDocImportProgressAllDone) Write(writer io.Writer, value DocImportProgressAllDone) {
	FfiConverterBytesINSTANCE.Write(writer, value.Key)
}

type FfiDestroyerDocImportProgressAllDone struct{}

func (_ FfiDestroyerDocImportProgressAllDone) Destroy(value DocImportProgressAllDone) {
	value.Destroy()
}

// A DocImportProgress event indicating a file was found with name `name`, from now on referred to via `id`
type DocImportProgressFound struct {
	// A new unique id for this entry.
	Id uint64
	// The name of the entry.
	Name string
	// The size of the entry in bytes.
	Size uint64
}

func (r *DocImportProgressFound) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Id)
	FfiDestroyerString{}.Destroy(r.Name)
	FfiDestroyerUint64{}.Destroy(r.Size)
}

type FfiConverterDocImportProgressFound struct{}

var FfiConverterDocImportProgressFoundINSTANCE = FfiConverterDocImportProgressFound{}

func (c FfiConverterDocImportProgressFound) Lift(rb RustBufferI) DocImportProgressFound {
	return LiftFromRustBuffer[DocImportProgressFound](c, rb)
}

func (c FfiConverterDocImportProgressFound) Read(reader io.Reader) DocImportProgressFound {
	return DocImportProgressFound{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterDocImportProgressFound) Lower(value DocImportProgressFound) C.RustBuffer {
	return LowerIntoRustBuffer[DocImportProgressFound](c, value)
}

func (c FfiConverterDocImportProgressFound) Write(writer io.Writer, value DocImportProgressFound) {
	FfiConverterUint64INSTANCE.Write(writer, value.Id)
	FfiConverterStringINSTANCE.Write(writer, value.Name)
	FfiConverterUint64INSTANCE.Write(writer, value.Size)
}

type FfiDestroyerDocImportProgressFound struct{}

func (_ FfiDestroyerDocImportProgressFound) Destroy(value DocImportProgressFound) {
	value.Destroy()
}

// A DocImportProgress event indicating we are finished adding `id` to the data store and the hash is `hash`.
type DocImportProgressIngestDone struct {
	// The unique id of the entry.
	Id uint64
	// The hash of the entry.
	Hash *Hash
}

func (r *DocImportProgressIngestDone) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Id)
	FfiDestroyerHash{}.Destroy(r.Hash)
}

type FfiConverterDocImportProgressIngestDone struct{}

var FfiConverterDocImportProgressIngestDoneINSTANCE = FfiConverterDocImportProgressIngestDone{}

func (c FfiConverterDocImportProgressIngestDone) Lift(rb RustBufferI) DocImportProgressIngestDone {
	return LiftFromRustBuffer[DocImportProgressIngestDone](c, rb)
}

func (c FfiConverterDocImportProgressIngestDone) Read(reader io.Reader) DocImportProgressIngestDone {
	return DocImportProgressIngestDone{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterHashINSTANCE.Read(reader),
	}
}

func (c FfiConverterDocImportProgressIngestDone) Lower(value DocImportProgressIngestDone) C.RustBuffer {
	return LowerIntoRustBuffer[DocImportProgressIngestDone](c, value)
}

func (c FfiConverterDocImportProgressIngestDone) Write(writer io.Writer, value DocImportProgressIngestDone) {
	FfiConverterUint64INSTANCE.Write(writer, value.Id)
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
}

type FfiDestroyerDocImportProgressIngestDone struct{}

func (_ FfiDestroyerDocImportProgressIngestDone) Destroy(value DocImportProgressIngestDone) {
	value.Destroy()
}

// A DocImportProgress event indicating we've made progress ingesting item `id`.
type DocImportProgressProgress struct {
	// The unique id of the entry.
	Id uint64
	// The offset of the progress, in bytes.
	Offset uint64
}

func (r *DocImportProgressProgress) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Id)
	FfiDestroyerUint64{}.Destroy(r.Offset)
}

type FfiConverterDocImportProgressProgress struct{}

var FfiConverterDocImportProgressProgressINSTANCE = FfiConverterDocImportProgressProgress{}

func (c FfiConverterDocImportProgressProgress) Lift(rb RustBufferI) DocImportProgressProgress {
	return LiftFromRustBuffer[DocImportProgressProgress](c, rb)
}

func (c FfiConverterDocImportProgressProgress) Read(reader io.Reader) DocImportProgressProgress {
	return DocImportProgressProgress{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterDocImportProgressProgress) Lower(value DocImportProgressProgress) C.RustBuffer {
	return LowerIntoRustBuffer[DocImportProgressProgress](c, value)
}

func (c FfiConverterDocImportProgressProgress) Write(writer io.Writer, value DocImportProgressProgress) {
	FfiConverterUint64INSTANCE.Write(writer, value.Id)
	FfiConverterUint64INSTANCE.Write(writer, value.Offset)
}

type FfiDestroyerDocImportProgressProgress struct{}

func (_ FfiDestroyerDocImportProgressProgress) Destroy(value DocImportProgressProgress) {
	value.Destroy()
}

// A DownloadProgress event indicating we got an error and need to abort
type DownloadProgressAbort struct {
	Error string
}

func (r *DownloadProgressAbort) Destroy() {
	FfiDestroyerString{}.Destroy(r.Error)
}

type FfiConverterDownloadProgressAbort struct{}

var FfiConverterDownloadProgressAbortINSTANCE = FfiConverterDownloadProgressAbort{}

func (c FfiConverterDownloadProgressAbort) Lift(rb RustBufferI) DownloadProgressAbort {
	return LiftFromRustBuffer[DownloadProgressAbort](c, rb)
}

func (c FfiConverterDownloadProgressAbort) Read(reader io.Reader) DownloadProgressAbort {
	return DownloadProgressAbort{
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterDownloadProgressAbort) Lower(value DownloadProgressAbort) C.RustBuffer {
	return LowerIntoRustBuffer[DownloadProgressAbort](c, value)
}

func (c FfiConverterDownloadProgressAbort) Write(writer io.Writer, value DownloadProgressAbort) {
	FfiConverterStringINSTANCE.Write(writer, value.Error)
}

type FfiDestroyerDownloadProgressAbort struct{}

func (_ FfiDestroyerDownloadProgressAbort) Destroy(value DownloadProgressAbort) {
	value.Destroy()
}

// A DownloadProgress event indicating we are done with the whole operation
type DownloadProgressAllDone struct {
	// The number of bytes written
	BytesWritten uint64
	// The number of bytes read
	BytesRead uint64
	// The time it took to transfer the data
	Elapsed time.Duration
}

func (r *DownloadProgressAllDone) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.BytesWritten)
	FfiDestroyerUint64{}.Destroy(r.BytesRead)
	FfiDestroyerDuration{}.Destroy(r.Elapsed)
}

type FfiConverterDownloadProgressAllDone struct{}

var FfiConverterDownloadProgressAllDoneINSTANCE = FfiConverterDownloadProgressAllDone{}

func (c FfiConverterDownloadProgressAllDone) Lift(rb RustBufferI) DownloadProgressAllDone {
	return LiftFromRustBuffer[DownloadProgressAllDone](c, rb)
}

func (c FfiConverterDownloadProgressAllDone) Read(reader io.Reader) DownloadProgressAllDone {
	return DownloadProgressAllDone{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterDurationINSTANCE.Read(reader),
	}
}

func (c FfiConverterDownloadProgressAllDone) Lower(value DownloadProgressAllDone) C.RustBuffer {
	return LowerIntoRustBuffer[DownloadProgressAllDone](c, value)
}

func (c FfiConverterDownloadProgressAllDone) Write(writer io.Writer, value DownloadProgressAllDone) {
	FfiConverterUint64INSTANCE.Write(writer, value.BytesWritten)
	FfiConverterUint64INSTANCE.Write(writer, value.BytesRead)
	FfiConverterDurationINSTANCE.Write(writer, value.Elapsed)
}

type FfiDestroyerDownloadProgressAllDone struct{}

func (_ FfiDestroyerDownloadProgressAllDone) Destroy(value DownloadProgressAllDone) {
	value.Destroy()
}

// A DownloadProgress event indicated we are done with `id`
type DownloadProgressDone struct {
	// The unique id of the entry.
	Id uint64
}

func (r *DownloadProgressDone) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Id)
}

type FfiConverterDownloadProgressDone struct{}

var FfiConverterDownloadProgressDoneINSTANCE = FfiConverterDownloadProgressDone{}

func (c FfiConverterDownloadProgressDone) Lift(rb RustBufferI) DownloadProgressDone {
	return LiftFromRustBuffer[DownloadProgressDone](c, rb)
}

func (c FfiConverterDownloadProgressDone) Read(reader io.Reader) DownloadProgressDone {
	return DownloadProgressDone{
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterDownloadProgressDone) Lower(value DownloadProgressDone) C.RustBuffer {
	return LowerIntoRustBuffer[DownloadProgressDone](c, value)
}

func (c FfiConverterDownloadProgressDone) Write(writer io.Writer, value DownloadProgressDone) {
	FfiConverterUint64INSTANCE.Write(writer, value.Id)
}

type FfiDestroyerDownloadProgressDone struct{}

func (_ FfiDestroyerDownloadProgressDone) Destroy(value DownloadProgressDone) {
	value.Destroy()
}

// A DownloadProgress event indicating an item was found with hash `hash`, that can be referred to by `id`
type DownloadProgressFound struct {
	// A new unique id for this entry.
	Id uint64
	// child offset
	Child uint64
	// The hash of the entry.
	Hash *Hash
	// The size of the entry in bytes.
	Size uint64
}

func (r *DownloadProgressFound) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Id)
	FfiDestroyerUint64{}.Destroy(r.Child)
	FfiDestroyerHash{}.Destroy(r.Hash)
	FfiDestroyerUint64{}.Destroy(r.Size)
}

type FfiConverterDownloadProgressFound struct{}

var FfiConverterDownloadProgressFoundINSTANCE = FfiConverterDownloadProgressFound{}

func (c FfiConverterDownloadProgressFound) Lift(rb RustBufferI) DownloadProgressFound {
	return LiftFromRustBuffer[DownloadProgressFound](c, rb)
}

func (c FfiConverterDownloadProgressFound) Read(reader io.Reader) DownloadProgressFound {
	return DownloadProgressFound{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterHashINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterDownloadProgressFound) Lower(value DownloadProgressFound) C.RustBuffer {
	return LowerIntoRustBuffer[DownloadProgressFound](c, value)
}

func (c FfiConverterDownloadProgressFound) Write(writer io.Writer, value DownloadProgressFound) {
	FfiConverterUint64INSTANCE.Write(writer, value.Id)
	FfiConverterUint64INSTANCE.Write(writer, value.Child)
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
	FfiConverterUint64INSTANCE.Write(writer, value.Size)
}

type FfiDestroyerDownloadProgressFound struct{}

func (_ FfiDestroyerDownloadProgressFound) Destroy(value DownloadProgressFound) {
	value.Destroy()
}

// A DownloadProgress event indicating an item was found with hash `hash`, that can be referred to by `id`
type DownloadProgressFoundHashSeq struct {
	// Number of children in the collection, if known.
	Children uint64
	// The hash of the entry.
	Hash *Hash
}

func (r *DownloadProgressFoundHashSeq) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Children)
	FfiDestroyerHash{}.Destroy(r.Hash)
}

type FfiConverterDownloadProgressFoundHashSeq struct{}

var FfiConverterDownloadProgressFoundHashSeqINSTANCE = FfiConverterDownloadProgressFoundHashSeq{}

func (c FfiConverterDownloadProgressFoundHashSeq) Lift(rb RustBufferI) DownloadProgressFoundHashSeq {
	return LiftFromRustBuffer[DownloadProgressFoundHashSeq](c, rb)
}

func (c FfiConverterDownloadProgressFoundHashSeq) Read(reader io.Reader) DownloadProgressFoundHashSeq {
	return DownloadProgressFoundHashSeq{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterHashINSTANCE.Read(reader),
	}
}

func (c FfiConverterDownloadProgressFoundHashSeq) Lower(value DownloadProgressFoundHashSeq) C.RustBuffer {
	return LowerIntoRustBuffer[DownloadProgressFoundHashSeq](c, value)
}

func (c FfiConverterDownloadProgressFoundHashSeq) Write(writer io.Writer, value DownloadProgressFoundHashSeq) {
	FfiConverterUint64INSTANCE.Write(writer, value.Children)
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
}

type FfiDestroyerDownloadProgressFoundHashSeq struct{}

func (_ FfiDestroyerDownloadProgressFoundHashSeq) Destroy(value DownloadProgressFoundHashSeq) {
	value.Destroy()
}

// A DownloadProgress event indicating an entry was found locally
type DownloadProgressFoundLocal struct {
	// child offset
	Child uint64
	// The hash of the entry.
	Hash *Hash
	// The size of the entry in bytes.
	Size uint64
	// The ranges that are available locally.
	ValidRanges *RangeSpec
}

func (r *DownloadProgressFoundLocal) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Child)
	FfiDestroyerHash{}.Destroy(r.Hash)
	FfiDestroyerUint64{}.Destroy(r.Size)
	FfiDestroyerRangeSpec{}.Destroy(r.ValidRanges)
}

type FfiConverterDownloadProgressFoundLocal struct{}

var FfiConverterDownloadProgressFoundLocalINSTANCE = FfiConverterDownloadProgressFoundLocal{}

func (c FfiConverterDownloadProgressFoundLocal) Lift(rb RustBufferI) DownloadProgressFoundLocal {
	return LiftFromRustBuffer[DownloadProgressFoundLocal](c, rb)
}

func (c FfiConverterDownloadProgressFoundLocal) Read(reader io.Reader) DownloadProgressFoundLocal {
	return DownloadProgressFoundLocal{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterHashINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterRangeSpecINSTANCE.Read(reader),
	}
}

func (c FfiConverterDownloadProgressFoundLocal) Lower(value DownloadProgressFoundLocal) C.RustBuffer {
	return LowerIntoRustBuffer[DownloadProgressFoundLocal](c, value)
}

func (c FfiConverterDownloadProgressFoundLocal) Write(writer io.Writer, value DownloadProgressFoundLocal) {
	FfiConverterUint64INSTANCE.Write(writer, value.Child)
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
	FfiConverterUint64INSTANCE.Write(writer, value.Size)
	FfiConverterRangeSpecINSTANCE.Write(writer, value.ValidRanges)
}

type FfiDestroyerDownloadProgressFoundLocal struct{}

func (_ FfiDestroyerDownloadProgressFoundLocal) Destroy(value DownloadProgressFoundLocal) {
	value.Destroy()
}

type DownloadProgressInitialState struct {
	// Whether we are connected to a node
	Connected bool
}

func (r *DownloadProgressInitialState) Destroy() {
	FfiDestroyerBool{}.Destroy(r.Connected)
}

type FfiConverterDownloadProgressInitialState struct{}

var FfiConverterDownloadProgressInitialStateINSTANCE = FfiConverterDownloadProgressInitialState{}

func (c FfiConverterDownloadProgressInitialState) Lift(rb RustBufferI) DownloadProgressInitialState {
	return LiftFromRustBuffer[DownloadProgressInitialState](c, rb)
}

func (c FfiConverterDownloadProgressInitialState) Read(reader io.Reader) DownloadProgressInitialState {
	return DownloadProgressInitialState{
		FfiConverterBoolINSTANCE.Read(reader),
	}
}

func (c FfiConverterDownloadProgressInitialState) Lower(value DownloadProgressInitialState) C.RustBuffer {
	return LowerIntoRustBuffer[DownloadProgressInitialState](c, value)
}

func (c FfiConverterDownloadProgressInitialState) Write(writer io.Writer, value DownloadProgressInitialState) {
	FfiConverterBoolINSTANCE.Write(writer, value.Connected)
}

type FfiDestroyerDownloadProgressInitialState struct{}

func (_ FfiDestroyerDownloadProgressInitialState) Destroy(value DownloadProgressInitialState) {
	value.Destroy()
}

// A DownloadProgress event indicating we got progress ingesting item `id`.
type DownloadProgressProgress struct {
	// The unique id of the entry.
	Id uint64
	// The offset of the progress, in bytes.
	Offset uint64
}

func (r *DownloadProgressProgress) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Id)
	FfiDestroyerUint64{}.Destroy(r.Offset)
}

type FfiConverterDownloadProgressProgress struct{}

var FfiConverterDownloadProgressProgressINSTANCE = FfiConverterDownloadProgressProgress{}

func (c FfiConverterDownloadProgressProgress) Lift(rb RustBufferI) DownloadProgressProgress {
	return LiftFromRustBuffer[DownloadProgressProgress](c, rb)
}

func (c FfiConverterDownloadProgressProgress) Read(reader io.Reader) DownloadProgressProgress {
	return DownloadProgressProgress{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterDownloadProgressProgress) Lower(value DownloadProgressProgress) C.RustBuffer {
	return LowerIntoRustBuffer[DownloadProgressProgress](c, value)
}

func (c FfiConverterDownloadProgressProgress) Write(writer io.Writer, value DownloadProgressProgress) {
	FfiConverterUint64INSTANCE.Write(writer, value.Id)
	FfiConverterUint64INSTANCE.Write(writer, value.Offset)
}

type FfiDestroyerDownloadProgressProgress struct{}

func (_ FfiDestroyerDownloadProgressProgress) Destroy(value DownloadProgressProgress) {
	value.Destroy()
}

// A request was received from a client.
type GetRequestReceived struct {
	// An unique connection id.
	ConnectionId uint64
	// An identifier uniquely identifying this transfer request.
	RequestId uint64
	// The hash for which the client wants to receive data.
	Hash *Hash
}

func (r *GetRequestReceived) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.ConnectionId)
	FfiDestroyerUint64{}.Destroy(r.RequestId)
	FfiDestroyerHash{}.Destroy(r.Hash)
}

type FfiConverterGetRequestReceived struct{}

var FfiConverterGetRequestReceivedINSTANCE = FfiConverterGetRequestReceived{}

func (c FfiConverterGetRequestReceived) Lift(rb RustBufferI) GetRequestReceived {
	return LiftFromRustBuffer[GetRequestReceived](c, rb)
}

func (c FfiConverterGetRequestReceived) Read(reader io.Reader) GetRequestReceived {
	return GetRequestReceived{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterHashINSTANCE.Read(reader),
	}
}

func (c FfiConverterGetRequestReceived) Lower(value GetRequestReceived) C.RustBuffer {
	return LowerIntoRustBuffer[GetRequestReceived](c, value)
}

func (c FfiConverterGetRequestReceived) Write(writer io.Writer, value GetRequestReceived) {
	FfiConverterUint64INSTANCE.Write(writer, value.ConnectionId)
	FfiConverterUint64INSTANCE.Write(writer, value.RequestId)
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
}

type FfiDestroyerGetRequestReceived struct{}

func (_ FfiDestroyerGetRequestReceived) Destroy(value GetRequestReceived) {
	value.Destroy()
}

// The Hash and associated tag of a newly created collection
type HashAndTag struct {
	// The hash of the collection
	Hash *Hash
	// The tag of the collection
	Tag []byte
}

func (r *HashAndTag) Destroy() {
	FfiDestroyerHash{}.Destroy(r.Hash)
	FfiDestroyerBytes{}.Destroy(r.Tag)
}

type FfiConverterHashAndTag struct{}

var FfiConverterHashAndTagINSTANCE = FfiConverterHashAndTag{}

func (c FfiConverterHashAndTag) Lift(rb RustBufferI) HashAndTag {
	return LiftFromRustBuffer[HashAndTag](c, rb)
}

func (c FfiConverterHashAndTag) Read(reader io.Reader) HashAndTag {
	return HashAndTag{
		FfiConverterHashINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterHashAndTag) Lower(value HashAndTag) C.RustBuffer {
	return LowerIntoRustBuffer[HashAndTag](c, value)
}

func (c FfiConverterHashAndTag) Write(writer io.Writer, value HashAndTag) {
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
	FfiConverterBytesINSTANCE.Write(writer, value.Tag)
}

type FfiDestroyerHashAndTag struct{}

func (_ FfiDestroyerHashAndTag) Destroy(value HashAndTag) {
	value.Destroy()
}

// A response to a list blobs request
type IncompleteBlobInfo struct {
	// The size we got
	Size uint64
	// The size we expect
	ExpectedSize uint64
	// The hash of the blob
	Hash *Hash
}

func (r *IncompleteBlobInfo) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Size)
	FfiDestroyerUint64{}.Destroy(r.ExpectedSize)
	FfiDestroyerHash{}.Destroy(r.Hash)
}

type FfiConverterIncompleteBlobInfo struct{}

var FfiConverterIncompleteBlobInfoINSTANCE = FfiConverterIncompleteBlobInfo{}

func (c FfiConverterIncompleteBlobInfo) Lift(rb RustBufferI) IncompleteBlobInfo {
	return LiftFromRustBuffer[IncompleteBlobInfo](c, rb)
}

func (c FfiConverterIncompleteBlobInfo) Read(reader io.Reader) IncompleteBlobInfo {
	return IncompleteBlobInfo{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterHashINSTANCE.Read(reader),
	}
}

func (c FfiConverterIncompleteBlobInfo) Lower(value IncompleteBlobInfo) C.RustBuffer {
	return LowerIntoRustBuffer[IncompleteBlobInfo](c, value)
}

func (c FfiConverterIncompleteBlobInfo) Write(writer io.Writer, value IncompleteBlobInfo) {
	FfiConverterUint64INSTANCE.Write(writer, value.Size)
	FfiConverterUint64INSTANCE.Write(writer, value.ExpectedSize)
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
}

type FfiDestroyerIncompleteBlobInfo struct{}

func (_ FfiDestroyerIncompleteBlobInfo) Destroy(value IncompleteBlobInfo) {
	value.Destroy()
}

// Outcome of an InsertRemove event.
type InsertRemoteEvent struct {
	// The peer that sent us the entry.
	From *PublicKey
	// The inserted entry.
	Entry *Entry
	// If the content is available at the local node
	ContentStatus ContentStatus
}

func (r *InsertRemoteEvent) Destroy() {
	FfiDestroyerPublicKey{}.Destroy(r.From)
	FfiDestroyerEntry{}.Destroy(r.Entry)
	FfiDestroyerContentStatus{}.Destroy(r.ContentStatus)
}

type FfiConverterInsertRemoteEvent struct{}

var FfiConverterInsertRemoteEventINSTANCE = FfiConverterInsertRemoteEvent{}

func (c FfiConverterInsertRemoteEvent) Lift(rb RustBufferI) InsertRemoteEvent {
	return LiftFromRustBuffer[InsertRemoteEvent](c, rb)
}

func (c FfiConverterInsertRemoteEvent) Read(reader io.Reader) InsertRemoteEvent {
	return InsertRemoteEvent{
		FfiConverterPublicKeyINSTANCE.Read(reader),
		FfiConverterEntryINSTANCE.Read(reader),
		FfiConverterContentStatusINSTANCE.Read(reader),
	}
}

func (c FfiConverterInsertRemoteEvent) Lower(value InsertRemoteEvent) C.RustBuffer {
	return LowerIntoRustBuffer[InsertRemoteEvent](c, value)
}

func (c FfiConverterInsertRemoteEvent) Write(writer io.Writer, value InsertRemoteEvent) {
	FfiConverterPublicKeyINSTANCE.Write(writer, value.From)
	FfiConverterEntryINSTANCE.Write(writer, value.Entry)
	FfiConverterContentStatusINSTANCE.Write(writer, value.ContentStatus)
}

type FfiDestroyerInsertRemoteEvent struct{}

func (_ FfiDestroyerInsertRemoteEvent) Destroy(value InsertRemoteEvent) {
	value.Destroy()
}

// The latency and type of the control message
type LatencyAndControlMsg struct {
	// The latency of the control message
	Latency time.Duration
	// The type of control message, represented as a string
	ControlMsg string
}

func (r *LatencyAndControlMsg) Destroy() {
	FfiDestroyerDuration{}.Destroy(r.Latency)
	FfiDestroyerString{}.Destroy(r.ControlMsg)
}

type FfiConverterLatencyAndControlMsg struct{}

var FfiConverterLatencyAndControlMsgINSTANCE = FfiConverterLatencyAndControlMsg{}

func (c FfiConverterLatencyAndControlMsg) Lift(rb RustBufferI) LatencyAndControlMsg {
	return LiftFromRustBuffer[LatencyAndControlMsg](c, rb)
}

func (c FfiConverterLatencyAndControlMsg) Read(reader io.Reader) LatencyAndControlMsg {
	return LatencyAndControlMsg{
		FfiConverterDurationINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterLatencyAndControlMsg) Lower(value LatencyAndControlMsg) C.RustBuffer {
	return LowerIntoRustBuffer[LatencyAndControlMsg](c, value)
}

func (c FfiConverterLatencyAndControlMsg) Write(writer io.Writer, value LatencyAndControlMsg) {
	FfiConverterDurationINSTANCE.Write(writer, value.Latency)
	FfiConverterStringINSTANCE.Write(writer, value.ControlMsg)
}

type FfiDestroyerLatencyAndControlMsg struct{}

func (_ FfiDestroyerLatencyAndControlMsg) Destroy(value LatencyAndControlMsg) {
	value.Destroy()
}

// `LinkAndName` includes a name and a hash for a blob in a collection
type LinkAndName struct {
	// The name associated with this [`Hash`]
	Name string
	// The [`Hash`] of the blob
	Link *Hash
}

func (r *LinkAndName) Destroy() {
	FfiDestroyerString{}.Destroy(r.Name)
	FfiDestroyerHash{}.Destroy(r.Link)
}

type FfiConverterLinkAndName struct{}

var FfiConverterLinkAndNameINSTANCE = FfiConverterLinkAndName{}

func (c FfiConverterLinkAndName) Lift(rb RustBufferI) LinkAndName {
	return LiftFromRustBuffer[LinkAndName](c, rb)
}

func (c FfiConverterLinkAndName) Read(reader io.Reader) LinkAndName {
	return LinkAndName{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterHashINSTANCE.Read(reader),
	}
}

func (c FfiConverterLinkAndName) Lower(value LinkAndName) C.RustBuffer {
	return LowerIntoRustBuffer[LinkAndName](c, value)
}

func (c FfiConverterLinkAndName) Write(writer io.Writer, value LinkAndName) {
	FfiConverterStringINSTANCE.Write(writer, value.Name)
	FfiConverterHashINSTANCE.Write(writer, value.Link)
}

type FfiDestroyerLinkAndName struct{}

func (_ FfiDestroyerLinkAndName) Destroy(value LinkAndName) {
	value.Destroy()
}

// The actual content of a gossip message.
type MessageContent struct {
	// The content of the message
	Content []byte
	// The node that delivered the message. This is not the same as the original author.
	DeliveredFrom string
}

func (r *MessageContent) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.Content)
	FfiDestroyerString{}.Destroy(r.DeliveredFrom)
}

type FfiConverterMessageContent struct{}

var FfiConverterMessageContentINSTANCE = FfiConverterMessageContent{}

func (c FfiConverterMessageContent) Lift(rb RustBufferI) MessageContent {
	return LiftFromRustBuffer[MessageContent](c, rb)
}

func (c FfiConverterMessageContent) Read(reader io.Reader) MessageContent {
	return MessageContent{
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterMessageContent) Lower(value MessageContent) C.RustBuffer {
	return LowerIntoRustBuffer[MessageContent](c, value)
}

func (c FfiConverterMessageContent) Write(writer io.Writer, value MessageContent) {
	FfiConverterBytesINSTANCE.Write(writer, value.Content)
	FfiConverterStringINSTANCE.Write(writer, value.DeliveredFrom)
}

type FfiDestroyerMessageContent struct{}

func (_ FfiDestroyerMessageContent) Destroy(value MessageContent) {
	value.Destroy()
}

// The namespace id and CapabilityKind (read/write) of the doc
type NamespaceAndCapability struct {
	// The namespace id of the doc
	Namespace string
	// The capability you have for the doc (read/write)
	Capability CapabilityKind
}

func (r *NamespaceAndCapability) Destroy() {
	FfiDestroyerString{}.Destroy(r.Namespace)
	FfiDestroyerCapabilityKind{}.Destroy(r.Capability)
}

type FfiConverterNamespaceAndCapability struct{}

var FfiConverterNamespaceAndCapabilityINSTANCE = FfiConverterNamespaceAndCapability{}

func (c FfiConverterNamespaceAndCapability) Lift(rb RustBufferI) NamespaceAndCapability {
	return LiftFromRustBuffer[NamespaceAndCapability](c, rb)
}

func (c FfiConverterNamespaceAndCapability) Read(reader io.Reader) NamespaceAndCapability {
	return NamespaceAndCapability{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterCapabilityKindINSTANCE.Read(reader),
	}
}

func (c FfiConverterNamespaceAndCapability) Lower(value NamespaceAndCapability) C.RustBuffer {
	return LowerIntoRustBuffer[NamespaceAndCapability](c, value)
}

func (c FfiConverterNamespaceAndCapability) Write(writer io.Writer, value NamespaceAndCapability) {
	FfiConverterStringINSTANCE.Write(writer, value.Namespace)
	FfiConverterCapabilityKindINSTANCE.Write(writer, value.Capability)
}

type FfiDestroyerNamespaceAndCapability struct{}

func (_ FfiDestroyerNamespaceAndCapability) Destroy(value NamespaceAndCapability) {
	value.Destroy()
}

// Options passed to [`IrohNode.new`]. Controls the behaviour of an iroh node.
type NodeOptions struct {
	// How frequently the blob store should clean up unreferenced blobs, in milliseconds.
	// Set to 0 to disable gc
	GcIntervalMillis *uint64
	// Provide a callback to hook into events when the blobs component adds and provides blobs.
	BlobEvents *BlobProvideEventCallback
	// Should docs be enabled? Defaults to `false`.
	EnableDocs bool
	// Overwrites the default IPv4 address to bind to
	Ipv4Addr *string
	// Overwrites the default IPv6 address to bind to
	Ipv6Addr *string
	// Configure the node discovery. Defaults to the default set of config
	NodeDiscovery *NodeDiscoveryConfig
	// Provide a specific secret key, identifying this node. Must be 32 bytes long.
	SecretKey *[]byte
	Protocols *map[string]ProtocolCreator
}

func (r *NodeOptions) Destroy() {
	FfiDestroyerOptionalUint64{}.Destroy(r.GcIntervalMillis)
	FfiDestroyerOptionalBlobProvideEventCallback{}.Destroy(r.BlobEvents)
	FfiDestroyerBool{}.Destroy(r.EnableDocs)
	FfiDestroyerOptionalString{}.Destroy(r.Ipv4Addr)
	FfiDestroyerOptionalString{}.Destroy(r.Ipv6Addr)
	FfiDestroyerOptionalNodeDiscoveryConfig{}.Destroy(r.NodeDiscovery)
	FfiDestroyerOptionalBytes{}.Destroy(r.SecretKey)
	FfiDestroyerOptionalMapBytesProtocolCreator{}.Destroy(r.Protocols)
}

type FfiConverterNodeOptions struct{}

var FfiConverterNodeOptionsINSTANCE = FfiConverterNodeOptions{}

func (c FfiConverterNodeOptions) Lift(rb RustBufferI) NodeOptions {
	return LiftFromRustBuffer[NodeOptions](c, rb)
}

func (c FfiConverterNodeOptions) Read(reader io.Reader) NodeOptions {
	return NodeOptions{
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalBlobProvideEventCallbackINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterOptionalNodeDiscoveryConfigINSTANCE.Read(reader),
		FfiConverterOptionalBytesINSTANCE.Read(reader),
		FfiConverterOptionalMapBytesProtocolCreatorINSTANCE.Read(reader),
	}
}

func (c FfiConverterNodeOptions) Lower(value NodeOptions) C.RustBuffer {
	return LowerIntoRustBuffer[NodeOptions](c, value)
}

func (c FfiConverterNodeOptions) Write(writer io.Writer, value NodeOptions) {
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.GcIntervalMillis)
	FfiConverterOptionalBlobProvideEventCallbackINSTANCE.Write(writer, value.BlobEvents)
	FfiConverterBoolINSTANCE.Write(writer, value.EnableDocs)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.Ipv4Addr)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.Ipv6Addr)
	FfiConverterOptionalNodeDiscoveryConfigINSTANCE.Write(writer, value.NodeDiscovery)
	FfiConverterOptionalBytesINSTANCE.Write(writer, value.SecretKey)
	FfiConverterOptionalMapBytesProtocolCreatorINSTANCE.Write(writer, value.Protocols)
}

type FfiDestroyerNodeOptions struct{}

func (_ FfiDestroyerNodeOptions) Destroy(value NodeOptions) {
	value.Destroy()
}

// The state for an open replica.
type OpenState struct {
	// Whether to accept sync requests for this replica.
	Sync bool
	// How many event subscriptions are open
	Subscribers uint64
	// By how many handles the replica is currently held open
	Handles uint64
}

func (r *OpenState) Destroy() {
	FfiDestroyerBool{}.Destroy(r.Sync)
	FfiDestroyerUint64{}.Destroy(r.Subscribers)
	FfiDestroyerUint64{}.Destroy(r.Handles)
}

type FfiConverterOpenState struct{}

var FfiConverterOpenStateINSTANCE = FfiConverterOpenState{}

func (c FfiConverterOpenState) Lift(rb RustBufferI) OpenState {
	return LiftFromRustBuffer[OpenState](c, rb)
}

func (c FfiConverterOpenState) Read(reader io.Reader) OpenState {
	return OpenState{
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterOpenState) Lower(value OpenState) C.RustBuffer {
	return LowerIntoRustBuffer[OpenState](c, value)
}

func (c FfiConverterOpenState) Write(writer io.Writer, value OpenState) {
	FfiConverterBoolINSTANCE.Write(writer, value.Sync)
	FfiConverterUint64INSTANCE.Write(writer, value.Subscribers)
	FfiConverterUint64INSTANCE.Write(writer, value.Handles)
}

type FfiDestroyerOpenState struct{}

func (_ FfiDestroyerOpenState) Destroy(value OpenState) {
	value.Destroy()
}

// Options for sorting and pagination for using [`Query`]s.
type QueryOptions struct {
	// Sort by author or key first.
	//
	// Default is [`SortBy::AuthorKey`], so sorting first by author and then by key.
	SortBy SortBy
	// Direction by which to sort the entries
	//
	// Default is [`SortDirection::Asc`]
	Direction SortDirection
	// Offset
	Offset uint64
	// Limit to limit the pagination.
	//
	// When the limit is 0, the limit does not exist.
	Limit uint64
}

func (r *QueryOptions) Destroy() {
	FfiDestroyerSortBy{}.Destroy(r.SortBy)
	FfiDestroyerSortDirection{}.Destroy(r.Direction)
	FfiDestroyerUint64{}.Destroy(r.Offset)
	FfiDestroyerUint64{}.Destroy(r.Limit)
}

type FfiConverterQueryOptions struct{}

var FfiConverterQueryOptionsINSTANCE = FfiConverterQueryOptions{}

func (c FfiConverterQueryOptions) Lift(rb RustBufferI) QueryOptions {
	return LiftFromRustBuffer[QueryOptions](c, rb)
}

func (c FfiConverterQueryOptions) Read(reader io.Reader) QueryOptions {
	return QueryOptions{
		FfiConverterSortByINSTANCE.Read(reader),
		FfiConverterSortDirectionINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterQueryOptions) Lower(value QueryOptions) C.RustBuffer {
	return LowerIntoRustBuffer[QueryOptions](c, value)
}

func (c FfiConverterQueryOptions) Write(writer io.Writer, value QueryOptions) {
	FfiConverterSortByINSTANCE.Write(writer, value.SortBy)
	FfiConverterSortDirectionINSTANCE.Write(writer, value.Direction)
	FfiConverterUint64INSTANCE.Write(writer, value.Offset)
	FfiConverterUint64INSTANCE.Write(writer, value.Limit)
}

type FfiDestroyerQueryOptions struct{}

func (_ FfiDestroyerQueryOptions) Destroy(value QueryOptions) {
	value.Destroy()
}

// Information about a remote node
type RemoteInfo struct {
	// The node identifier of the endpoint. Also a public key.
	NodeId *PublicKey
	// Relay url, if available.
	RelayUrl *string
	// List of addresses at which this node might be reachable, plus any latency information we
	// have about that address and the last time the address was used.
	Addrs []*DirectAddrInfo
	// The type of connection we have to the peer, either direct or over relay.
	ConnType *ConnectionType
	// The latency of the `conn_type`.
	Latency *time.Duration
	// Duration since the last time this peer was used.
	LastUsed *time.Duration
}

func (r *RemoteInfo) Destroy() {
	FfiDestroyerPublicKey{}.Destroy(r.NodeId)
	FfiDestroyerOptionalString{}.Destroy(r.RelayUrl)
	FfiDestroyerSequenceDirectAddrInfo{}.Destroy(r.Addrs)
	FfiDestroyerConnectionType{}.Destroy(r.ConnType)
	FfiDestroyerOptionalDuration{}.Destroy(r.Latency)
	FfiDestroyerOptionalDuration{}.Destroy(r.LastUsed)
}

type FfiConverterRemoteInfo struct{}

var FfiConverterRemoteInfoINSTANCE = FfiConverterRemoteInfo{}

func (c FfiConverterRemoteInfo) Lift(rb RustBufferI) RemoteInfo {
	return LiftFromRustBuffer[RemoteInfo](c, rb)
}

func (c FfiConverterRemoteInfo) Read(reader io.Reader) RemoteInfo {
	return RemoteInfo{
		FfiConverterPublicKeyINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterSequenceDirectAddrInfoINSTANCE.Read(reader),
		FfiConverterConnectionTypeINSTANCE.Read(reader),
		FfiConverterOptionalDurationINSTANCE.Read(reader),
		FfiConverterOptionalDurationINSTANCE.Read(reader),
	}
}

func (c FfiConverterRemoteInfo) Lower(value RemoteInfo) C.RustBuffer {
	return LowerIntoRustBuffer[RemoteInfo](c, value)
}

func (c FfiConverterRemoteInfo) Write(writer io.Writer, value RemoteInfo) {
	FfiConverterPublicKeyINSTANCE.Write(writer, value.NodeId)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.RelayUrl)
	FfiConverterSequenceDirectAddrInfoINSTANCE.Write(writer, value.Addrs)
	FfiConverterConnectionTypeINSTANCE.Write(writer, value.ConnType)
	FfiConverterOptionalDurationINSTANCE.Write(writer, value.Latency)
	FfiConverterOptionalDurationINSTANCE.Write(writer, value.LastUsed)
}

type FfiDestroyerRemoteInfo struct{}

func (_ FfiDestroyerRemoteInfo) Destroy(value RemoteInfo) {
	value.Destroy()
}

// Outcome of a sync operation
type SyncEvent struct {
	// Peer we synced with
	Peer *PublicKey
	// Origin of the sync exchange
	Origin Origin
	// Timestamp when the sync finished
	Finished time.Time
	// Timestamp when the sync started
	Started time.Time
	// Result of the sync operation. `None` if successfull.
	Result *string
}

func (r *SyncEvent) Destroy() {
	FfiDestroyerPublicKey{}.Destroy(r.Peer)
	FfiDestroyerOrigin{}.Destroy(r.Origin)
	FfiDestroyerTimestamp{}.Destroy(r.Finished)
	FfiDestroyerTimestamp{}.Destroy(r.Started)
	FfiDestroyerOptionalString{}.Destroy(r.Result)
}

type FfiConverterSyncEvent struct{}

var FfiConverterSyncEventINSTANCE = FfiConverterSyncEvent{}

func (c FfiConverterSyncEvent) Lift(rb RustBufferI) SyncEvent {
	return LiftFromRustBuffer[SyncEvent](c, rb)
}

func (c FfiConverterSyncEvent) Read(reader io.Reader) SyncEvent {
	return SyncEvent{
		FfiConverterPublicKeyINSTANCE.Read(reader),
		FfiConverterOriginINSTANCE.Read(reader),
		FfiConverterTimestampINSTANCE.Read(reader),
		FfiConverterTimestampINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterSyncEvent) Lower(value SyncEvent) C.RustBuffer {
	return LowerIntoRustBuffer[SyncEvent](c, value)
}

func (c FfiConverterSyncEvent) Write(writer io.Writer, value SyncEvent) {
	FfiConverterPublicKeyINSTANCE.Write(writer, value.Peer)
	FfiConverterOriginINSTANCE.Write(writer, value.Origin)
	FfiConverterTimestampINSTANCE.Write(writer, value.Finished)
	FfiConverterTimestampINSTANCE.Write(writer, value.Started)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.Result)
}

type FfiDestroyerSyncEvent struct{}

func (_ FfiDestroyerSyncEvent) Destroy(value SyncEvent) {
	value.Destroy()
}

// A response to a list collections request
type TagInfo struct {
	// The tag
	Name []byte
	// The format of the associated blob
	Format BlobFormat
	// The hash of the associated blob
	Hash *Hash
}

func (r *TagInfo) Destroy() {
	FfiDestroyerBytes{}.Destroy(r.Name)
	FfiDestroyerBlobFormat{}.Destroy(r.Format)
	FfiDestroyerHash{}.Destroy(r.Hash)
}

type FfiConverterTagInfo struct{}

var FfiConverterTagInfoINSTANCE = FfiConverterTagInfo{}

func (c FfiConverterTagInfo) Lift(rb RustBufferI) TagInfo {
	return LiftFromRustBuffer[TagInfo](c, rb)
}

func (c FfiConverterTagInfo) Read(reader io.Reader) TagInfo {
	return TagInfo{
		FfiConverterBytesINSTANCE.Read(reader),
		FfiConverterBlobFormatINSTANCE.Read(reader),
		FfiConverterHashINSTANCE.Read(reader),
	}
}

func (c FfiConverterTagInfo) Lower(value TagInfo) C.RustBuffer {
	return LowerIntoRustBuffer[TagInfo](c, value)
}

func (c FfiConverterTagInfo) Write(writer io.Writer, value TagInfo) {
	FfiConverterBytesINSTANCE.Write(writer, value.Name)
	FfiConverterBlobFormatINSTANCE.Write(writer, value.Format)
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
}

type FfiDestroyerTagInfo struct{}

func (_ FfiDestroyerTagInfo) Destroy(value TagInfo) {
	value.Destroy()
}

// An BlobProvide event indicating a new tagged blob or collection was added
type TaggedBlobAdded struct {
	// The hash of the added data
	Hash *Hash
	// The format of the added data
	Format BlobFormat
	// The tag of the added data
	Tag []byte
}

func (r *TaggedBlobAdded) Destroy() {
	FfiDestroyerHash{}.Destroy(r.Hash)
	FfiDestroyerBlobFormat{}.Destroy(r.Format)
	FfiDestroyerBytes{}.Destroy(r.Tag)
}

type FfiConverterTaggedBlobAdded struct{}

var FfiConverterTaggedBlobAddedINSTANCE = FfiConverterTaggedBlobAdded{}

func (c FfiConverterTaggedBlobAdded) Lift(rb RustBufferI) TaggedBlobAdded {
	return LiftFromRustBuffer[TaggedBlobAdded](c, rb)
}

func (c FfiConverterTaggedBlobAdded) Read(reader io.Reader) TaggedBlobAdded {
	return TaggedBlobAdded{
		FfiConverterHashINSTANCE.Read(reader),
		FfiConverterBlobFormatINSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterTaggedBlobAdded) Lower(value TaggedBlobAdded) C.RustBuffer {
	return LowerIntoRustBuffer[TaggedBlobAdded](c, value)
}

func (c FfiConverterTaggedBlobAdded) Write(writer io.Writer, value TaggedBlobAdded) {
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
	FfiConverterBlobFormatINSTANCE.Write(writer, value.Format)
	FfiConverterBytesINSTANCE.Write(writer, value.Tag)
}

type FfiDestroyerTaggedBlobAdded struct{}

func (_ FfiDestroyerTaggedBlobAdded) Destroy(value TaggedBlobAdded) {
	value.Destroy()
}

// A request was aborted because the client disconnected.
type TransferAborted struct {
	// The quic connection id.
	ConnectionId uint64
	// An identifier uniquely identifying this request.
	RequestId uint64
	// statistics about the transfer. This is None if the transfer
	// was aborted before any data was sent.
	Stats *TransferStats
}

func (r *TransferAborted) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.ConnectionId)
	FfiDestroyerUint64{}.Destroy(r.RequestId)
	FfiDestroyerOptionalTransferStats{}.Destroy(r.Stats)
}

type FfiConverterTransferAborted struct{}

var FfiConverterTransferAbortedINSTANCE = FfiConverterTransferAborted{}

func (c FfiConverterTransferAborted) Lift(rb RustBufferI) TransferAborted {
	return LiftFromRustBuffer[TransferAborted](c, rb)
}

func (c FfiConverterTransferAborted) Read(reader io.Reader) TransferAborted {
	return TransferAborted{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterOptionalTransferStatsINSTANCE.Read(reader),
	}
}

func (c FfiConverterTransferAborted) Lower(value TransferAborted) C.RustBuffer {
	return LowerIntoRustBuffer[TransferAborted](c, value)
}

func (c FfiConverterTransferAborted) Write(writer io.Writer, value TransferAborted) {
	FfiConverterUint64INSTANCE.Write(writer, value.ConnectionId)
	FfiConverterUint64INSTANCE.Write(writer, value.RequestId)
	FfiConverterOptionalTransferStatsINSTANCE.Write(writer, value.Stats)
}

type FfiDestroyerTransferAborted struct{}

func (_ FfiDestroyerTransferAborted) Destroy(value TransferAborted) {
	value.Destroy()
}

// A blob in a sequence was transferred.
type TransferBlobCompleted struct {
	// An unique connection id.
	ConnectionId uint64
	// An identifier uniquely identifying this transfer request.
	RequestId uint64
	// The hash of the blob
	Hash *Hash
	// The index of the blob in the sequence.
	Index uint64
	// The size of the blob transferred.
	Size uint64
}

func (r *TransferBlobCompleted) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.ConnectionId)
	FfiDestroyerUint64{}.Destroy(r.RequestId)
	FfiDestroyerHash{}.Destroy(r.Hash)
	FfiDestroyerUint64{}.Destroy(r.Index)
	FfiDestroyerUint64{}.Destroy(r.Size)
}

type FfiConverterTransferBlobCompleted struct{}

var FfiConverterTransferBlobCompletedINSTANCE = FfiConverterTransferBlobCompleted{}

func (c FfiConverterTransferBlobCompleted) Lift(rb RustBufferI) TransferBlobCompleted {
	return LiftFromRustBuffer[TransferBlobCompleted](c, rb)
}

func (c FfiConverterTransferBlobCompleted) Read(reader io.Reader) TransferBlobCompleted {
	return TransferBlobCompleted{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterHashINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterTransferBlobCompleted) Lower(value TransferBlobCompleted) C.RustBuffer {
	return LowerIntoRustBuffer[TransferBlobCompleted](c, value)
}

func (c FfiConverterTransferBlobCompleted) Write(writer io.Writer, value TransferBlobCompleted) {
	FfiConverterUint64INSTANCE.Write(writer, value.ConnectionId)
	FfiConverterUint64INSTANCE.Write(writer, value.RequestId)
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
	FfiConverterUint64INSTANCE.Write(writer, value.Index)
	FfiConverterUint64INSTANCE.Write(writer, value.Size)
}

type FfiDestroyerTransferBlobCompleted struct{}

func (_ FfiDestroyerTransferBlobCompleted) Destroy(value TransferBlobCompleted) {
	value.Destroy()
}

// A request was completed and the data was sent to the client.
type TransferCompleted struct {
	// An unique connection id.
	ConnectionId uint64
	// An identifier uniquely identifying this transfer request.
	RequestId uint64
	// statistics about the transfer
	Stats TransferStats
}

func (r *TransferCompleted) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.ConnectionId)
	FfiDestroyerUint64{}.Destroy(r.RequestId)
	FfiDestroyerTransferStats{}.Destroy(r.Stats)
}

type FfiConverterTransferCompleted struct{}

var FfiConverterTransferCompletedINSTANCE = FfiConverterTransferCompleted{}

func (c FfiConverterTransferCompleted) Lift(rb RustBufferI) TransferCompleted {
	return LiftFromRustBuffer[TransferCompleted](c, rb)
}

func (c FfiConverterTransferCompleted) Read(reader io.Reader) TransferCompleted {
	return TransferCompleted{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterTransferStatsINSTANCE.Read(reader),
	}
}

func (c FfiConverterTransferCompleted) Lower(value TransferCompleted) C.RustBuffer {
	return LowerIntoRustBuffer[TransferCompleted](c, value)
}

func (c FfiConverterTransferCompleted) Write(writer io.Writer, value TransferCompleted) {
	FfiConverterUint64INSTANCE.Write(writer, value.ConnectionId)
	FfiConverterUint64INSTANCE.Write(writer, value.RequestId)
	FfiConverterTransferStatsINSTANCE.Write(writer, value.Stats)
}

type FfiDestroyerTransferCompleted struct{}

func (_ FfiDestroyerTransferCompleted) Destroy(value TransferCompleted) {
	value.Destroy()
}

// A sequence of hashes has been found and is being transferred.
type TransferHashSeqStarted struct {
	// An unique connection id.
	ConnectionId uint64
	// An identifier uniquely identifying this transfer request.
	RequestId uint64
	// The number of blobs in the sequence.
	NumBlobs uint64
}

func (r *TransferHashSeqStarted) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.ConnectionId)
	FfiDestroyerUint64{}.Destroy(r.RequestId)
	FfiDestroyerUint64{}.Destroy(r.NumBlobs)
}

type FfiConverterTransferHashSeqStarted struct{}

var FfiConverterTransferHashSeqStartedINSTANCE = FfiConverterTransferHashSeqStarted{}

func (c FfiConverterTransferHashSeqStarted) Lift(rb RustBufferI) TransferHashSeqStarted {
	return LiftFromRustBuffer[TransferHashSeqStarted](c, rb)
}

func (c FfiConverterTransferHashSeqStarted) Read(reader io.Reader) TransferHashSeqStarted {
	return TransferHashSeqStarted{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterTransferHashSeqStarted) Lower(value TransferHashSeqStarted) C.RustBuffer {
	return LowerIntoRustBuffer[TransferHashSeqStarted](c, value)
}

func (c FfiConverterTransferHashSeqStarted) Write(writer io.Writer, value TransferHashSeqStarted) {
	FfiConverterUint64INSTANCE.Write(writer, value.ConnectionId)
	FfiConverterUint64INSTANCE.Write(writer, value.RequestId)
	FfiConverterUint64INSTANCE.Write(writer, value.NumBlobs)
}

type FfiDestroyerTransferHashSeqStarted struct{}

func (_ FfiDestroyerTransferHashSeqStarted) Destroy(value TransferHashSeqStarted) {
	value.Destroy()
}

// A chunk of a blob was transferred.
//
// These events will be sent with try_send, so you can not assume that you
// will receive all of them.
type TransferProgress struct {
	// An unique connection id.
	ConnectionId uint64
	// An identifier uniquely identifying this transfer request.
	RequestId uint64
	// The hash for which we are transferring data.
	Hash *Hash
	// Offset up to which we have transferred data.
	EndOffset uint64
}

func (r *TransferProgress) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.ConnectionId)
	FfiDestroyerUint64{}.Destroy(r.RequestId)
	FfiDestroyerHash{}.Destroy(r.Hash)
	FfiDestroyerUint64{}.Destroy(r.EndOffset)
}

type FfiConverterTransferProgress struct{}

var FfiConverterTransferProgressINSTANCE = FfiConverterTransferProgress{}

func (c FfiConverterTransferProgress) Lift(rb RustBufferI) TransferProgress {
	return LiftFromRustBuffer[TransferProgress](c, rb)
}

func (c FfiConverterTransferProgress) Read(reader io.Reader) TransferProgress {
	return TransferProgress{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterHashINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterTransferProgress) Lower(value TransferProgress) C.RustBuffer {
	return LowerIntoRustBuffer[TransferProgress](c, value)
}

func (c FfiConverterTransferProgress) Write(writer io.Writer, value TransferProgress) {
	FfiConverterUint64INSTANCE.Write(writer, value.ConnectionId)
	FfiConverterUint64INSTANCE.Write(writer, value.RequestId)
	FfiConverterHashINSTANCE.Write(writer, value.Hash)
	FfiConverterUint64INSTANCE.Write(writer, value.EndOffset)
}

type FfiDestroyerTransferProgress struct{}

func (_ FfiDestroyerTransferProgress) Destroy(value TransferProgress) {
	value.Destroy()
}

// The stats for a transfer of a collection or blob.
type TransferStats struct {
	// The total duration of the transfer in milliseconds
	Duration uint64
}

func (r *TransferStats) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Duration)
}

type FfiConverterTransferStats struct{}

var FfiConverterTransferStatsINSTANCE = FfiConverterTransferStats{}

func (c FfiConverterTransferStats) Lift(rb RustBufferI) TransferStats {
	return LiftFromRustBuffer[TransferStats](c, rb)
}

func (c FfiConverterTransferStats) Read(reader io.Reader) TransferStats {
	return TransferStats{
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterTransferStats) Lower(value TransferStats) C.RustBuffer {
	return LowerIntoRustBuffer[TransferStats](c, value)
}

func (c FfiConverterTransferStats) Write(writer io.Writer, value TransferStats) {
	FfiConverterUint64INSTANCE.Write(writer, value.Duration)
}

type FfiDestroyerTransferStats struct{}

func (_ FfiDestroyerTransferStats) Destroy(value TransferStats) {
	value.Destroy()
}

// The different types of AddProgress events
type AddProgressType uint

const (
	// An item was found with name `name`, from now on referred to via `id`
	AddProgressTypeFound AddProgressType = 1
	// We got progress ingesting item `id`.
	AddProgressTypeProgress AddProgressType = 2
	// We are done with `id`, and the hash is `hash`.
	AddProgressTypeDone AddProgressType = 3
	// We are done with the whole operation.
	AddProgressTypeAllDone AddProgressType = 4
	// We got an error and need to abort.
	//
	// This will be the last message in the stream.
	AddProgressTypeAbort AddProgressType = 5
)

type FfiConverterAddProgressType struct{}

var FfiConverterAddProgressTypeINSTANCE = FfiConverterAddProgressType{}

func (c FfiConverterAddProgressType) Lift(rb RustBufferI) AddProgressType {
	return LiftFromRustBuffer[AddProgressType](c, rb)
}

func (c FfiConverterAddProgressType) Lower(value AddProgressType) C.RustBuffer {
	return LowerIntoRustBuffer[AddProgressType](c, value)
}
func (FfiConverterAddProgressType) Read(reader io.Reader) AddProgressType {
	id := readInt32(reader)
	return AddProgressType(id)
}

func (FfiConverterAddProgressType) Write(writer io.Writer, value AddProgressType) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerAddProgressType struct{}

func (_ FfiDestroyerAddProgressType) Destroy(value AddProgressType) {
}

// Options when creating a ticket
type AddrInfoOptions uint

const (
	// Only the Node ID is added.
	//
	// This usually means that iroh-dns discovery is used to find address information.
	AddrInfoOptionsId AddrInfoOptions = 1
	// Include both the relay URL and the direct addresses.
	AddrInfoOptionsRelayAndAddresses AddrInfoOptions = 2
	// Only include the relay URL.
	AddrInfoOptionsRelay AddrInfoOptions = 3
	// Only include the direct addresses.
	AddrInfoOptionsAddresses AddrInfoOptions = 4
)

type FfiConverterAddrInfoOptions struct{}

var FfiConverterAddrInfoOptionsINSTANCE = FfiConverterAddrInfoOptions{}

func (c FfiConverterAddrInfoOptions) Lift(rb RustBufferI) AddrInfoOptions {
	return LiftFromRustBuffer[AddrInfoOptions](c, rb)
}

func (c FfiConverterAddrInfoOptions) Lower(value AddrInfoOptions) C.RustBuffer {
	return LowerIntoRustBuffer[AddrInfoOptions](c, value)
}
func (FfiConverterAddrInfoOptions) Read(reader io.Reader) AddrInfoOptions {
	id := readInt32(reader)
	return AddrInfoOptions(id)
}

func (FfiConverterAddrInfoOptions) Write(writer io.Writer, value AddrInfoOptions) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerAddrInfoOptions struct{}

func (_ FfiDestroyerAddrInfoOptions) Destroy(value AddrInfoOptions) {
}

// The expected format of a hash being exported.
type BlobExportFormat uint

const (
	// The hash refers to any blob and will be exported to a single file.
	BlobExportFormatBlob BlobExportFormat = 1
	// The hash refers to a [`crate::format::collection::Collection`] blob
	// and all children of the collection shall be exported to one file per child.
	//
	// If the blob can be parsed as a [`BlobFormat::HashSeq`], and the first child contains
	// collection metadata, all other children of the collection will be exported to
	// a file each, with their collection name treated as a relative path to the export
	// destination path.
	//
	// If the blob cannot be parsed as a collection, the operation will fail.
	BlobExportFormatCollection BlobExportFormat = 2
)

type FfiConverterBlobExportFormat struct{}

var FfiConverterBlobExportFormatINSTANCE = FfiConverterBlobExportFormat{}

func (c FfiConverterBlobExportFormat) Lift(rb RustBufferI) BlobExportFormat {
	return LiftFromRustBuffer[BlobExportFormat](c, rb)
}

func (c FfiConverterBlobExportFormat) Lower(value BlobExportFormat) C.RustBuffer {
	return LowerIntoRustBuffer[BlobExportFormat](c, value)
}
func (FfiConverterBlobExportFormat) Read(reader io.Reader) BlobExportFormat {
	id := readInt32(reader)
	return BlobExportFormat(id)
}

func (FfiConverterBlobExportFormat) Write(writer io.Writer, value BlobExportFormat) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerBlobExportFormat struct{}

func (_ FfiDestroyerBlobExportFormat) Destroy(value BlobExportFormat) {
}

// The export mode describes how files will be exported.
//
// This is a hint to the import trait method. For some implementations, this
// does not make any sense. E.g. an in memory implementation will always have
// to copy the file into memory. Also, a disk based implementation might choose
// to copy small files even if the mode is `Reference`.
type BlobExportMode uint

const (
	// This mode will copy the file to the target directory.
	//
	// This is the safe default because the file can not be accidentally modified
	// after it has been exported.
	BlobExportModeCopy BlobExportMode = 1
	// This mode will try to move the file to the target directory and then reference it from
	// the database.
	//
	// This has a large performance and storage benefit, but it is less safe since
	// the file might be modified in the target directory after it has been exported.
	//
	// Stores are allowed to ignore this mode and always copy the file, e.g.
	// if the file is very small or if the store does not support referencing files.
	BlobExportModeTryReference BlobExportMode = 2
)

type FfiConverterBlobExportMode struct{}

var FfiConverterBlobExportModeINSTANCE = FfiConverterBlobExportMode{}

func (c FfiConverterBlobExportMode) Lift(rb RustBufferI) BlobExportMode {
	return LiftFromRustBuffer[BlobExportMode](c, rb)
}

func (c FfiConverterBlobExportMode) Lower(value BlobExportMode) C.RustBuffer {
	return LowerIntoRustBuffer[BlobExportMode](c, value)
}
func (FfiConverterBlobExportMode) Read(reader io.Reader) BlobExportMode {
	id := readInt32(reader)
	return BlobExportMode(id)
}

func (FfiConverterBlobExportMode) Write(writer io.Writer, value BlobExportMode) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerBlobExportMode struct{}

func (_ FfiDestroyerBlobExportMode) Destroy(value BlobExportMode) {
}

// A format identifier
type BlobFormat uint

const (
	// Raw blob
	BlobFormatRaw BlobFormat = 1
	// A sequence of BLAKE3 hashes
	BlobFormatHashSeq BlobFormat = 2
)

type FfiConverterBlobFormat struct{}

var FfiConverterBlobFormatINSTANCE = FfiConverterBlobFormat{}

func (c FfiConverterBlobFormat) Lift(rb RustBufferI) BlobFormat {
	return LiftFromRustBuffer[BlobFormat](c, rb)
}

func (c FfiConverterBlobFormat) Lower(value BlobFormat) C.RustBuffer {
	return LowerIntoRustBuffer[BlobFormat](c, value)
}
func (FfiConverterBlobFormat) Read(reader io.Reader) BlobFormat {
	id := readInt32(reader)
	return BlobFormat(id)
}

func (FfiConverterBlobFormat) Write(writer io.Writer, value BlobFormat) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerBlobFormat struct{}

func (_ FfiDestroyerBlobFormat) Destroy(value BlobFormat) {
}

// The different types of BlobProvide events
type BlobProvideEventType uint

const (
	// A new collection or tagged blob has been added
	BlobProvideEventTypeTaggedBlobAdded BlobProvideEventType = 1
	// A new client connected to the node.
	BlobProvideEventTypeClientConnected BlobProvideEventType = 2
	// A request was received from a client.
	BlobProvideEventTypeGetRequestReceived BlobProvideEventType = 3
	// A sequence of hashes has been found and is being transferred.
	BlobProvideEventTypeTransferHashSeqStarted BlobProvideEventType = 4
	// A chunk of a blob was transferred.
	//
	// it is not safe to assume all progress events will be sent
	BlobProvideEventTypeTransferProgress BlobProvideEventType = 5
	// A blob in a sequence was transferred.
	BlobProvideEventTypeTransferBlobCompleted BlobProvideEventType = 6
	// A request was completed and the data was sent to the client.
	BlobProvideEventTypeTransferCompleted BlobProvideEventType = 7
	// A request was aborted because the client disconnected.
	BlobProvideEventTypeTransferAborted BlobProvideEventType = 8
)

type FfiConverterBlobProvideEventType struct{}

var FfiConverterBlobProvideEventTypeINSTANCE = FfiConverterBlobProvideEventType{}

func (c FfiConverterBlobProvideEventType) Lift(rb RustBufferI) BlobProvideEventType {
	return LiftFromRustBuffer[BlobProvideEventType](c, rb)
}

func (c FfiConverterBlobProvideEventType) Lower(value BlobProvideEventType) C.RustBuffer {
	return LowerIntoRustBuffer[BlobProvideEventType](c, value)
}
func (FfiConverterBlobProvideEventType) Read(reader io.Reader) BlobProvideEventType {
	id := readInt32(reader)
	return BlobProvideEventType(id)
}

func (FfiConverterBlobProvideEventType) Write(writer io.Writer, value BlobProvideEventType) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerBlobProvideEventType struct{}

func (_ FfiDestroyerBlobProvideEventType) Destroy(value BlobProvideEventType) {
}

type CallbackError struct {
	err error
}

// Convience method to turn *CallbackError into error
// Avoiding treating nil pointer as non nil error interface
func (err *CallbackError) AsError() error {
	if err == nil {
		return nil
	} else {
		return err
	}
}

func (err CallbackError) Error() string {
	return fmt.Sprintf("CallbackError: %s", err.err.Error())
}

func (err CallbackError) Unwrap() error {
	return err.err
}

// Err* are used for checking error type with `errors.Is`
var ErrCallbackErrorError = fmt.Errorf("CallbackErrorError")

// Variant structs
type CallbackErrorError struct {
}

func NewCallbackErrorError() *CallbackError {
	return &CallbackError{err: &CallbackErrorError{}}
}

func (e CallbackErrorError) destroy() {
}

func (err CallbackErrorError) Error() string {
	return fmt.Sprint("Error")
}

func (self CallbackErrorError) Is(target error) bool {
	return target == ErrCallbackErrorError
}

type FfiConverterCallbackError struct{}

var FfiConverterCallbackErrorINSTANCE = FfiConverterCallbackError{}

func (c FfiConverterCallbackError) Lift(eb RustBufferI) *CallbackError {
	return LiftFromRustBuffer[*CallbackError](c, eb)
}

func (c FfiConverterCallbackError) Lower(value *CallbackError) C.RustBuffer {
	return LowerIntoRustBuffer[*CallbackError](c, value)
}

func (c FfiConverterCallbackError) Read(reader io.Reader) *CallbackError {
	errorID := readUint32(reader)

	switch errorID {
	case 1:
		return &CallbackError{&CallbackErrorError{}}
	default:
		panic(fmt.Sprintf("Unknown error code %d in FfiConverterCallbackError.Read()", errorID))
	}
}

func (c FfiConverterCallbackError) Write(writer io.Writer, value *CallbackError) {
	switch variantValue := value.err.(type) {
	case *CallbackErrorError:
		writeInt32(writer, 1)
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiConverterCallbackError.Write", value))
	}
}

type FfiDestroyerCallbackError struct{}

func (_ FfiDestroyerCallbackError) Destroy(value *CallbackError) {
	switch variantValue := value.err.(type) {
	case CallbackErrorError:
		variantValue.destroy()
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiDestroyerCallbackError.Destroy", value))
	}
}

type CapabilityKind uint

const (
	// A writable replica.
	CapabilityKindWrite CapabilityKind = 1
	// A readable replica.
	CapabilityKindRead CapabilityKind = 2
)

type FfiConverterCapabilityKind struct{}

var FfiConverterCapabilityKindINSTANCE = FfiConverterCapabilityKind{}

func (c FfiConverterCapabilityKind) Lift(rb RustBufferI) CapabilityKind {
	return LiftFromRustBuffer[CapabilityKind](c, rb)
}

func (c FfiConverterCapabilityKind) Lower(value CapabilityKind) C.RustBuffer {
	return LowerIntoRustBuffer[CapabilityKind](c, value)
}
func (FfiConverterCapabilityKind) Read(reader io.Reader) CapabilityKind {
	id := readInt32(reader)
	return CapabilityKind(id)
}

func (FfiConverterCapabilityKind) Write(writer io.Writer, value CapabilityKind) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerCapabilityKind struct{}

func (_ FfiDestroyerCapabilityKind) Destroy(value CapabilityKind) {
}

// The type of the connection
type ConnType uint

const (
	// Indicates you have a UDP connection.
	ConnTypeDirect ConnType = 1
	// Indicates you have a relayed connection.
	ConnTypeRelay ConnType = 2
	// Indicates you have an unverified UDP connection, and a relay connection for backup.
	ConnTypeMixed ConnType = 3
	// Indicates you have no proof of connection.
	ConnTypeNone ConnType = 4
)

type FfiConverterConnType struct{}

var FfiConverterConnTypeINSTANCE = FfiConverterConnType{}

func (c FfiConverterConnType) Lift(rb RustBufferI) ConnType {
	return LiftFromRustBuffer[ConnType](c, rb)
}

func (c FfiConverterConnType) Lower(value ConnType) C.RustBuffer {
	return LowerIntoRustBuffer[ConnType](c, value)
}
func (FfiConverterConnType) Read(reader io.Reader) ConnType {
	id := readInt32(reader)
	return ConnType(id)
}

func (FfiConverterConnType) Write(writer io.Writer, value ConnType) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerConnType struct{}

func (_ FfiDestroyerConnType) Destroy(value ConnType) {
}

// Whether the content status is available on a node.
type ContentStatus uint

const (
	// The content is completely available.
	ContentStatusComplete ContentStatus = 1
	// The content is partially available.
	ContentStatusIncomplete ContentStatus = 2
	// The content is missing.
	ContentStatusMissing ContentStatus = 3
)

type FfiConverterContentStatus struct{}

var FfiConverterContentStatusINSTANCE = FfiConverterContentStatus{}

func (c FfiConverterContentStatus) Lift(rb RustBufferI) ContentStatus {
	return LiftFromRustBuffer[ContentStatus](c, rb)
}

func (c FfiConverterContentStatus) Lower(value ContentStatus) C.RustBuffer {
	return LowerIntoRustBuffer[ContentStatus](c, value)
}
func (FfiConverterContentStatus) Read(reader io.Reader) ContentStatus {
	id := readInt32(reader)
	return ContentStatus(id)
}

func (FfiConverterContentStatus) Write(writer io.Writer, value ContentStatus) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerContentStatus struct{}

func (_ FfiDestroyerContentStatus) Destroy(value ContentStatus) {
}

// The type of `DocExportProgress` event
type DocExportProgressType uint

const (
	// An item was found with name `name`, from now on referred to via `id`
	DocExportProgressTypeFound DocExportProgressType = 1
	// We got progress exporting item `id`.
	DocExportProgressTypeProgress DocExportProgressType = 2
	// We finished exporting a blob with `id`
	DocExportProgressTypeDone DocExportProgressType = 3
	// We are done writing the entry to the filesystem
	DocExportProgressTypeAllDone DocExportProgressType = 4
	// We got an error and need to abort.
	//
	// This will be the last message in the stream.
	DocExportProgressTypeAbort DocExportProgressType = 5
)

type FfiConverterDocExportProgressType struct{}

var FfiConverterDocExportProgressTypeINSTANCE = FfiConverterDocExportProgressType{}

func (c FfiConverterDocExportProgressType) Lift(rb RustBufferI) DocExportProgressType {
	return LiftFromRustBuffer[DocExportProgressType](c, rb)
}

func (c FfiConverterDocExportProgressType) Lower(value DocExportProgressType) C.RustBuffer {
	return LowerIntoRustBuffer[DocExportProgressType](c, value)
}
func (FfiConverterDocExportProgressType) Read(reader io.Reader) DocExportProgressType {
	id := readInt32(reader)
	return DocExportProgressType(id)
}

func (FfiConverterDocExportProgressType) Write(writer io.Writer, value DocExportProgressType) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerDocExportProgressType struct{}

func (_ FfiDestroyerDocExportProgressType) Destroy(value DocExportProgressType) {
}

// The type of `DocImportProgress` event
type DocImportProgressType uint

const (
	// An item was found with name `name`, from now on referred to via `id`
	DocImportProgressTypeFound DocImportProgressType = 1
	// We got progress ingesting item `id`.
	DocImportProgressTypeProgress DocImportProgressType = 2
	// We are done ingesting `id`, and the hash is `hash`.
	DocImportProgressTypeIngestDone DocImportProgressType = 3
	// We are done with the whole operation.
	DocImportProgressTypeAllDone DocImportProgressType = 4
	// We got an error and need to abort.
	//
	// This will be the last message in the stream.
	DocImportProgressTypeAbort DocImportProgressType = 5
)

type FfiConverterDocImportProgressType struct{}

var FfiConverterDocImportProgressTypeINSTANCE = FfiConverterDocImportProgressType{}

func (c FfiConverterDocImportProgressType) Lift(rb RustBufferI) DocImportProgressType {
	return LiftFromRustBuffer[DocImportProgressType](c, rb)
}

func (c FfiConverterDocImportProgressType) Lower(value DocImportProgressType) C.RustBuffer {
	return LowerIntoRustBuffer[DocImportProgressType](c, value)
}
func (FfiConverterDocImportProgressType) Read(reader io.Reader) DocImportProgressType {
	id := readInt32(reader)
	return DocImportProgressType(id)
}

func (FfiConverterDocImportProgressType) Write(writer io.Writer, value DocImportProgressType) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerDocImportProgressType struct{}

func (_ FfiDestroyerDocImportProgressType) Destroy(value DocImportProgressType) {
}

// The different types of DownloadProgress events
type DownloadProgressType uint

const (
	DownloadProgressTypeInitialState DownloadProgressType = 1
	DownloadProgressTypeFoundLocal   DownloadProgressType = 2
	DownloadProgressTypeConnected    DownloadProgressType = 3
	DownloadProgressTypeFound        DownloadProgressType = 4
	DownloadProgressTypeFoundHashSeq DownloadProgressType = 5
	DownloadProgressTypeProgress     DownloadProgressType = 6
	DownloadProgressTypeDone         DownloadProgressType = 7
	DownloadProgressTypeAllDone      DownloadProgressType = 8
	DownloadProgressTypeAbort        DownloadProgressType = 9
)

type FfiConverterDownloadProgressType struct{}

var FfiConverterDownloadProgressTypeINSTANCE = FfiConverterDownloadProgressType{}

func (c FfiConverterDownloadProgressType) Lift(rb RustBufferI) DownloadProgressType {
	return LiftFromRustBuffer[DownloadProgressType](c, rb)
}

func (c FfiConverterDownloadProgressType) Lower(value DownloadProgressType) C.RustBuffer {
	return LowerIntoRustBuffer[DownloadProgressType](c, value)
}
func (FfiConverterDownloadProgressType) Read(reader io.Reader) DownloadProgressType {
	id := readInt32(reader)
	return DownloadProgressType(id)
}

func (FfiConverterDownloadProgressType) Write(writer io.Writer, value DownloadProgressType) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerDownloadProgressType struct{}

func (_ FfiDestroyerDownloadProgressType) Destroy(value DownloadProgressType) {
}

// The type of events that can be emitted during the live sync progress
type LiveEventType uint

const (
	// A local insertion.
	LiveEventTypeInsertLocal LiveEventType = 1
	// Received a remote insert.
	LiveEventTypeInsertRemote LiveEventType = 2
	// The content of an entry was downloaded and is now available at the local node
	LiveEventTypeContentReady LiveEventType = 3
	// We have a new neighbor in the swarm.
	LiveEventTypeNeighborUp LiveEventType = 4
	// We lost a neighbor in the swarm.
	LiveEventTypeNeighborDown LiveEventType = 5
	// A set-reconciliation sync finished.
	LiveEventTypeSyncFinished LiveEventType = 6
	// All pending content is now ready.
	//
	// This event signals that all queued content downloads from the last sync run have either
	// completed or failed.
	//
	// It will only be emitted after a [`Self::SyncFinished`] event, never before.
	//
	// Receiving this event does not guarantee that all content in the document is available. If
	// blobs failed to download, this event will still be emitted after all operations completed.
	LiveEventTypePendingContentReady LiveEventType = 7
)

type FfiConverterLiveEventType struct{}

var FfiConverterLiveEventTypeINSTANCE = FfiConverterLiveEventType{}

func (c FfiConverterLiveEventType) Lift(rb RustBufferI) LiveEventType {
	return LiftFromRustBuffer[LiveEventType](c, rb)
}

func (c FfiConverterLiveEventType) Lower(value LiveEventType) C.RustBuffer {
	return LowerIntoRustBuffer[LiveEventType](c, value)
}
func (FfiConverterLiveEventType) Read(reader io.Reader) LiveEventType {
	id := readInt32(reader)
	return LiveEventType(id)
}

func (FfiConverterLiveEventType) Write(writer io.Writer, value LiveEventType) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerLiveEventType struct{}

func (_ FfiDestroyerLiveEventType) Destroy(value LiveEventType) {
}

// The logging level. See the rust (log crate)[https://docs.rs/log] for more information.
type LogLevel uint

const (
	LogLevelTrace LogLevel = 1
	LogLevelDebug LogLevel = 2
	LogLevelInfo  LogLevel = 3
	LogLevelWarn  LogLevel = 4
	LogLevelError LogLevel = 5
	LogLevelOff   LogLevel = 6
)

type FfiConverterLogLevel struct{}

var FfiConverterLogLevelINSTANCE = FfiConverterLogLevel{}

func (c FfiConverterLogLevel) Lift(rb RustBufferI) LogLevel {
	return LiftFromRustBuffer[LogLevel](c, rb)
}

func (c FfiConverterLogLevel) Lower(value LogLevel) C.RustBuffer {
	return LowerIntoRustBuffer[LogLevel](c, value)
}
func (FfiConverterLogLevel) Read(reader io.Reader) LogLevel {
	id := readInt32(reader)
	return LogLevel(id)
}

func (FfiConverterLogLevel) Write(writer io.Writer, value LogLevel) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerLogLevel struct{}

func (_ FfiDestroyerLogLevel) Destroy(value LogLevel) {
}

type MessageType uint

const (
	MessageTypeNeighborUp   MessageType = 1
	MessageTypeNeighborDown MessageType = 2
	MessageTypeReceived     MessageType = 3
	MessageTypeJoined       MessageType = 4
	MessageTypeLagged       MessageType = 5
	MessageTypeError        MessageType = 6
)

type FfiConverterMessageType struct{}

var FfiConverterMessageTypeINSTANCE = FfiConverterMessageType{}

func (c FfiConverterMessageType) Lift(rb RustBufferI) MessageType {
	return LiftFromRustBuffer[MessageType](c, rb)
}

func (c FfiConverterMessageType) Lower(value MessageType) C.RustBuffer {
	return LowerIntoRustBuffer[MessageType](c, value)
}
func (FfiConverterMessageType) Read(reader io.Reader) MessageType {
	id := readInt32(reader)
	return MessageType(id)
}

func (FfiConverterMessageType) Write(writer io.Writer, value MessageType) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerMessageType struct{}

func (_ FfiDestroyerMessageType) Destroy(value MessageType) {
}

type NodeDiscoveryConfig uint

const (
	// Use no node discovery mechanism.
	NodeDiscoveryConfigNone NodeDiscoveryConfig = 1
	// Use the default discovery mechanism.
	//
	// This uses two discovery services concurrently:
	//
	// - It publishes to a pkarr service operated by [number 0] which makes the information
	// available via DNS in the `iroh.link` domain.
	//
	// - It uses an mDNS-like system to announce itself on the local network.
	//
	// # Usage during tests
	//
	// Note that the default changes when compiling with `cfg(test)` or the `test-utils`
	// cargo feature from [iroh-net] is enabled.  In this case only the Pkarr/DNS service
	// is used, but on the `iroh.test` domain.  This domain is not integrated with the
	// global DNS network and thus node discovery is effectively disabled.  To use node
	// discovery in a test use the [`iroh_net::test_utils::DnsPkarrServer`] in the test and
	// configure it here as a custom discovery mechanism ([`DiscoveryConfig::Custom`]).
	//
	// [number 0]: https://n0.computer
	NodeDiscoveryConfigDefault NodeDiscoveryConfig = 2
)

type FfiConverterNodeDiscoveryConfig struct{}

var FfiConverterNodeDiscoveryConfigINSTANCE = FfiConverterNodeDiscoveryConfig{}

func (c FfiConverterNodeDiscoveryConfig) Lift(rb RustBufferI) NodeDiscoveryConfig {
	return LiftFromRustBuffer[NodeDiscoveryConfig](c, rb)
}

func (c FfiConverterNodeDiscoveryConfig) Lower(value NodeDiscoveryConfig) C.RustBuffer {
	return LowerIntoRustBuffer[NodeDiscoveryConfig](c, value)
}
func (FfiConverterNodeDiscoveryConfig) Read(reader io.Reader) NodeDiscoveryConfig {
	id := readInt32(reader)
	return NodeDiscoveryConfig(id)
}

func (FfiConverterNodeDiscoveryConfig) Write(writer io.Writer, value NodeDiscoveryConfig) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerNodeDiscoveryConfig struct{}

func (_ FfiDestroyerNodeDiscoveryConfig) Destroy(value NodeDiscoveryConfig) {
}

// Why we performed a sync exchange
type Origin interface {
	Destroy()
}

// public, use a unit variant
type OriginConnect struct {
	Reason SyncReason
}

func (e OriginConnect) Destroy() {
	FfiDestroyerSyncReason{}.Destroy(e.Reason)
}

// A peer connected to us and we accepted the exchange
type OriginAccept struct {
}

func (e OriginAccept) Destroy() {
}

type FfiConverterOrigin struct{}

var FfiConverterOriginINSTANCE = FfiConverterOrigin{}

func (c FfiConverterOrigin) Lift(rb RustBufferI) Origin {
	return LiftFromRustBuffer[Origin](c, rb)
}

func (c FfiConverterOrigin) Lower(value Origin) C.RustBuffer {
	return LowerIntoRustBuffer[Origin](c, value)
}
func (FfiConverterOrigin) Read(reader io.Reader) Origin {
	id := readInt32(reader)
	switch id {
	case 1:
		return OriginConnect{
			FfiConverterSyncReasonINSTANCE.Read(reader),
		}
	case 2:
		return OriginAccept{}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterOrigin.Read()", id))
	}
}

func (FfiConverterOrigin) Write(writer io.Writer, value Origin) {
	switch variant_value := value.(type) {
	case OriginConnect:
		writeInt32(writer, 1)
		FfiConverterSyncReasonINSTANCE.Write(writer, variant_value.Reason)
	case OriginAccept:
		writeInt32(writer, 2)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterOrigin.Write", value))
	}
}

type FfiDestroyerOrigin struct{}

func (_ FfiDestroyerOrigin) Destroy(value Origin) {
	value.Destroy()
}

// Intended capability for document share tickets
type ShareMode uint

const (
	// Read-only access
	ShareModeRead ShareMode = 1
	// Write access
	ShareModeWrite ShareMode = 2
)

type FfiConverterShareMode struct{}

var FfiConverterShareModeINSTANCE = FfiConverterShareMode{}

func (c FfiConverterShareMode) Lift(rb RustBufferI) ShareMode {
	return LiftFromRustBuffer[ShareMode](c, rb)
}

func (c FfiConverterShareMode) Lower(value ShareMode) C.RustBuffer {
	return LowerIntoRustBuffer[ShareMode](c, value)
}
func (FfiConverterShareMode) Read(reader io.Reader) ShareMode {
	id := readInt32(reader)
	return ShareMode(id)
}

func (FfiConverterShareMode) Write(writer io.Writer, value ShareMode) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerShareMode struct{}

func (_ FfiDestroyerShareMode) Destroy(value ShareMode) {
}

// d Fields by which the query can be sorted
type SortBy uint

const (
	// Sort by key, then author.
	SortByKeyAuthor SortBy = 1
	// Sort by author, then key.
	SortByAuthorKey SortBy = 2
)

type FfiConverterSortBy struct{}

var FfiConverterSortByINSTANCE = FfiConverterSortBy{}

func (c FfiConverterSortBy) Lift(rb RustBufferI) SortBy {
	return LiftFromRustBuffer[SortBy](c, rb)
}

func (c FfiConverterSortBy) Lower(value SortBy) C.RustBuffer {
	return LowerIntoRustBuffer[SortBy](c, value)
}
func (FfiConverterSortBy) Read(reader io.Reader) SortBy {
	id := readInt32(reader)
	return SortBy(id)
}

func (FfiConverterSortBy) Write(writer io.Writer, value SortBy) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerSortBy struct{}

func (_ FfiDestroyerSortBy) Destroy(value SortBy) {
}

// Sort direction
type SortDirection uint

const (
	// Sort ascending
	SortDirectionAsc SortDirection = 1
	// Sort descending
	SortDirectionDesc SortDirection = 2
)

type FfiConverterSortDirection struct{}

var FfiConverterSortDirectionINSTANCE = FfiConverterSortDirection{}

func (c FfiConverterSortDirection) Lift(rb RustBufferI) SortDirection {
	return LiftFromRustBuffer[SortDirection](c, rb)
}

func (c FfiConverterSortDirection) Lower(value SortDirection) C.RustBuffer {
	return LowerIntoRustBuffer[SortDirection](c, value)
}
func (FfiConverterSortDirection) Read(reader io.Reader) SortDirection {
	id := readInt32(reader)
	return SortDirection(id)
}

func (FfiConverterSortDirection) Write(writer io.Writer, value SortDirection) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerSortDirection struct{}

func (_ FfiDestroyerSortDirection) Destroy(value SortDirection) {
}

// Why we started a sync request
type SyncReason uint

const (
	// Direct join request via API
	SyncReasonDirectJoin SyncReason = 1
	// Peer showed up as new neighbor in the gossip swarm
	SyncReasonNewNeighbor SyncReason = 2
	// We synced after receiving a sync report that indicated news for us
	SyncReasonSyncReport SyncReason = 3
	// We received a sync report while a sync was running, so run again afterwars
	SyncReasonResync SyncReason = 4
)

type FfiConverterSyncReason struct{}

var FfiConverterSyncReasonINSTANCE = FfiConverterSyncReason{}

func (c FfiConverterSyncReason) Lift(rb RustBufferI) SyncReason {
	return LiftFromRustBuffer[SyncReason](c, rb)
}

func (c FfiConverterSyncReason) Lower(value SyncReason) C.RustBuffer {
	return LowerIntoRustBuffer[SyncReason](c, value)
}
func (FfiConverterSyncReason) Read(reader io.Reader) SyncReason {
	id := readInt32(reader)
	return SyncReason(id)
}

func (FfiConverterSyncReason) Write(writer io.Writer, value SyncReason) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerSyncReason struct{}

func (_ FfiDestroyerSyncReason) Destroy(value SyncReason) {
}

type FfiConverterOptionalUint64 struct{}

var FfiConverterOptionalUint64INSTANCE = FfiConverterOptionalUint64{}

func (c FfiConverterOptionalUint64) Lift(rb RustBufferI) *uint64 {
	return LiftFromRustBuffer[*uint64](c, rb)
}

func (_ FfiConverterOptionalUint64) Read(reader io.Reader) *uint64 {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterUint64INSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalUint64) Lower(value *uint64) C.RustBuffer {
	return LowerIntoRustBuffer[*uint64](c, value)
}

func (_ FfiConverterOptionalUint64) Write(writer io.Writer, value *uint64) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterUint64INSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalUint64 struct{}

func (_ FfiDestroyerOptionalUint64) Destroy(value *uint64) {
	if value != nil {
		FfiDestroyerUint64{}.Destroy(*value)
	}
}

type FfiConverterOptionalString struct{}

var FfiConverterOptionalStringINSTANCE = FfiConverterOptionalString{}

func (c FfiConverterOptionalString) Lift(rb RustBufferI) *string {
	return LiftFromRustBuffer[*string](c, rb)
}

func (_ FfiConverterOptionalString) Read(reader io.Reader) *string {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterStringINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalString) Lower(value *string) C.RustBuffer {
	return LowerIntoRustBuffer[*string](c, value)
}

func (_ FfiConverterOptionalString) Write(writer io.Writer, value *string) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterStringINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalString struct{}

func (_ FfiDestroyerOptionalString) Destroy(value *string) {
	if value != nil {
		FfiDestroyerString{}.Destroy(*value)
	}
}

type FfiConverterOptionalBytes struct{}

var FfiConverterOptionalBytesINSTANCE = FfiConverterOptionalBytes{}

func (c FfiConverterOptionalBytes) Lift(rb RustBufferI) *[]byte {
	return LiftFromRustBuffer[*[]byte](c, rb)
}

func (_ FfiConverterOptionalBytes) Read(reader io.Reader) *[]byte {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterBytesINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalBytes) Lower(value *[]byte) C.RustBuffer {
	return LowerIntoRustBuffer[*[]byte](c, value)
}

func (_ FfiConverterOptionalBytes) Write(writer io.Writer, value *[]byte) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterBytesINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalBytes struct{}

func (_ FfiDestroyerOptionalBytes) Destroy(value *[]byte) {
	if value != nil {
		FfiDestroyerBytes{}.Destroy(*value)
	}
}

type FfiConverterOptionalDuration struct{}

var FfiConverterOptionalDurationINSTANCE = FfiConverterOptionalDuration{}

func (c FfiConverterOptionalDuration) Lift(rb RustBufferI) *time.Duration {
	return LiftFromRustBuffer[*time.Duration](c, rb)
}

func (_ FfiConverterOptionalDuration) Read(reader io.Reader) *time.Duration {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterDurationINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalDuration) Lower(value *time.Duration) C.RustBuffer {
	return LowerIntoRustBuffer[*time.Duration](c, value)
}

func (_ FfiConverterOptionalDuration) Write(writer io.Writer, value *time.Duration) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterDurationINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalDuration struct{}

func (_ FfiDestroyerOptionalDuration) Destroy(value *time.Duration) {
	if value != nil {
		FfiDestroyerDuration{}.Destroy(*value)
	}
}

type FfiConverterOptionalBlobProvideEventCallback struct{}

var FfiConverterOptionalBlobProvideEventCallbackINSTANCE = FfiConverterOptionalBlobProvideEventCallback{}

func (c FfiConverterOptionalBlobProvideEventCallback) Lift(rb RustBufferI) *BlobProvideEventCallback {
	return LiftFromRustBuffer[*BlobProvideEventCallback](c, rb)
}

func (_ FfiConverterOptionalBlobProvideEventCallback) Read(reader io.Reader) *BlobProvideEventCallback {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterBlobProvideEventCallbackINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalBlobProvideEventCallback) Lower(value *BlobProvideEventCallback) C.RustBuffer {
	return LowerIntoRustBuffer[*BlobProvideEventCallback](c, value)
}

func (_ FfiConverterOptionalBlobProvideEventCallback) Write(writer io.Writer, value *BlobProvideEventCallback) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterBlobProvideEventCallbackINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalBlobProvideEventCallback struct{}

func (_ FfiDestroyerOptionalBlobProvideEventCallback) Destroy(value *BlobProvideEventCallback) {
	if value != nil {
		FfiDestroyerBlobProvideEventCallback{}.Destroy(*value)
	}
}

type FfiConverterOptionalDoc struct{}

var FfiConverterOptionalDocINSTANCE = FfiConverterOptionalDoc{}

func (c FfiConverterOptionalDoc) Lift(rb RustBufferI) **Doc {
	return LiftFromRustBuffer[**Doc](c, rb)
}

func (_ FfiConverterOptionalDoc) Read(reader io.Reader) **Doc {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterDocINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalDoc) Lower(value **Doc) C.RustBuffer {
	return LowerIntoRustBuffer[**Doc](c, value)
}

func (_ FfiConverterOptionalDoc) Write(writer io.Writer, value **Doc) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterDocINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalDoc struct{}

func (_ FfiDestroyerOptionalDoc) Destroy(value **Doc) {
	if value != nil {
		FfiDestroyerDoc{}.Destroy(*value)
	}
}

type FfiConverterOptionalDocExportFileCallback struct{}

var FfiConverterOptionalDocExportFileCallbackINSTANCE = FfiConverterOptionalDocExportFileCallback{}

func (c FfiConverterOptionalDocExportFileCallback) Lift(rb RustBufferI) *DocExportFileCallback {
	return LiftFromRustBuffer[*DocExportFileCallback](c, rb)
}

func (_ FfiConverterOptionalDocExportFileCallback) Read(reader io.Reader) *DocExportFileCallback {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterDocExportFileCallbackINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalDocExportFileCallback) Lower(value *DocExportFileCallback) C.RustBuffer {
	return LowerIntoRustBuffer[*DocExportFileCallback](c, value)
}

func (_ FfiConverterOptionalDocExportFileCallback) Write(writer io.Writer, value *DocExportFileCallback) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterDocExportFileCallbackINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalDocExportFileCallback struct{}

func (_ FfiDestroyerOptionalDocExportFileCallback) Destroy(value *DocExportFileCallback) {
	if value != nil {
		FfiDestroyerDocExportFileCallback{}.Destroy(*value)
	}
}

type FfiConverterOptionalDocImportFileCallback struct{}

var FfiConverterOptionalDocImportFileCallbackINSTANCE = FfiConverterOptionalDocImportFileCallback{}

func (c FfiConverterOptionalDocImportFileCallback) Lift(rb RustBufferI) *DocImportFileCallback {
	return LiftFromRustBuffer[*DocImportFileCallback](c, rb)
}

func (_ FfiConverterOptionalDocImportFileCallback) Read(reader io.Reader) *DocImportFileCallback {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterDocImportFileCallbackINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalDocImportFileCallback) Lower(value *DocImportFileCallback) C.RustBuffer {
	return LowerIntoRustBuffer[*DocImportFileCallback](c, value)
}

func (_ FfiConverterOptionalDocImportFileCallback) Write(writer io.Writer, value *DocImportFileCallback) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterDocImportFileCallbackINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalDocImportFileCallback struct{}

func (_ FfiDestroyerOptionalDocImportFileCallback) Destroy(value *DocImportFileCallback) {
	if value != nil {
		FfiDestroyerDocImportFileCallback{}.Destroy(*value)
	}
}

type FfiConverterOptionalEntry struct{}

var FfiConverterOptionalEntryINSTANCE = FfiConverterOptionalEntry{}

func (c FfiConverterOptionalEntry) Lift(rb RustBufferI) **Entry {
	return LiftFromRustBuffer[**Entry](c, rb)
}

func (_ FfiConverterOptionalEntry) Read(reader io.Reader) **Entry {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterEntryINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalEntry) Lower(value **Entry) C.RustBuffer {
	return LowerIntoRustBuffer[**Entry](c, value)
}

func (_ FfiConverterOptionalEntry) Write(writer io.Writer, value **Entry) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterEntryINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalEntry struct{}

func (_ FfiDestroyerOptionalEntry) Destroy(value **Entry) {
	if value != nil {
		FfiDestroyerEntry{}.Destroy(*value)
	}
}

type FfiConverterOptionalLatencyAndControlMsg struct{}

var FfiConverterOptionalLatencyAndControlMsgINSTANCE = FfiConverterOptionalLatencyAndControlMsg{}

func (c FfiConverterOptionalLatencyAndControlMsg) Lift(rb RustBufferI) *LatencyAndControlMsg {
	return LiftFromRustBuffer[*LatencyAndControlMsg](c, rb)
}

func (_ FfiConverterOptionalLatencyAndControlMsg) Read(reader io.Reader) *LatencyAndControlMsg {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterLatencyAndControlMsgINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalLatencyAndControlMsg) Lower(value *LatencyAndControlMsg) C.RustBuffer {
	return LowerIntoRustBuffer[*LatencyAndControlMsg](c, value)
}

func (_ FfiConverterOptionalLatencyAndControlMsg) Write(writer io.Writer, value *LatencyAndControlMsg) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterLatencyAndControlMsgINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalLatencyAndControlMsg struct{}

func (_ FfiDestroyerOptionalLatencyAndControlMsg) Destroy(value *LatencyAndControlMsg) {
	if value != nil {
		FfiDestroyerLatencyAndControlMsg{}.Destroy(*value)
	}
}

type FfiConverterOptionalQueryOptions struct{}

var FfiConverterOptionalQueryOptionsINSTANCE = FfiConverterOptionalQueryOptions{}

func (c FfiConverterOptionalQueryOptions) Lift(rb RustBufferI) *QueryOptions {
	return LiftFromRustBuffer[*QueryOptions](c, rb)
}

func (_ FfiConverterOptionalQueryOptions) Read(reader io.Reader) *QueryOptions {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterQueryOptionsINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalQueryOptions) Lower(value *QueryOptions) C.RustBuffer {
	return LowerIntoRustBuffer[*QueryOptions](c, value)
}

func (_ FfiConverterOptionalQueryOptions) Write(writer io.Writer, value *QueryOptions) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterQueryOptionsINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalQueryOptions struct{}

func (_ FfiDestroyerOptionalQueryOptions) Destroy(value *QueryOptions) {
	if value != nil {
		FfiDestroyerQueryOptions{}.Destroy(*value)
	}
}

type FfiConverterOptionalRemoteInfo struct{}

var FfiConverterOptionalRemoteInfoINSTANCE = FfiConverterOptionalRemoteInfo{}

func (c FfiConverterOptionalRemoteInfo) Lift(rb RustBufferI) *RemoteInfo {
	return LiftFromRustBuffer[*RemoteInfo](c, rb)
}

func (_ FfiConverterOptionalRemoteInfo) Read(reader io.Reader) *RemoteInfo {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterRemoteInfoINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalRemoteInfo) Lower(value *RemoteInfo) C.RustBuffer {
	return LowerIntoRustBuffer[*RemoteInfo](c, value)
}

func (_ FfiConverterOptionalRemoteInfo) Write(writer io.Writer, value *RemoteInfo) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterRemoteInfoINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalRemoteInfo struct{}

func (_ FfiDestroyerOptionalRemoteInfo) Destroy(value *RemoteInfo) {
	if value != nil {
		FfiDestroyerRemoteInfo{}.Destroy(*value)
	}
}

type FfiConverterOptionalTransferStats struct{}

var FfiConverterOptionalTransferStatsINSTANCE = FfiConverterOptionalTransferStats{}

func (c FfiConverterOptionalTransferStats) Lift(rb RustBufferI) *TransferStats {
	return LiftFromRustBuffer[*TransferStats](c, rb)
}

func (_ FfiConverterOptionalTransferStats) Read(reader io.Reader) *TransferStats {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTransferStatsINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTransferStats) Lower(value *TransferStats) C.RustBuffer {
	return LowerIntoRustBuffer[*TransferStats](c, value)
}

func (_ FfiConverterOptionalTransferStats) Write(writer io.Writer, value *TransferStats) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTransferStatsINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTransferStats struct{}

func (_ FfiDestroyerOptionalTransferStats) Destroy(value *TransferStats) {
	if value != nil {
		FfiDestroyerTransferStats{}.Destroy(*value)
	}
}

type FfiConverterOptionalNodeDiscoveryConfig struct{}

var FfiConverterOptionalNodeDiscoveryConfigINSTANCE = FfiConverterOptionalNodeDiscoveryConfig{}

func (c FfiConverterOptionalNodeDiscoveryConfig) Lift(rb RustBufferI) *NodeDiscoveryConfig {
	return LiftFromRustBuffer[*NodeDiscoveryConfig](c, rb)
}

func (_ FfiConverterOptionalNodeDiscoveryConfig) Read(reader io.Reader) *NodeDiscoveryConfig {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterNodeDiscoveryConfigINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalNodeDiscoveryConfig) Lower(value *NodeDiscoveryConfig) C.RustBuffer {
	return LowerIntoRustBuffer[*NodeDiscoveryConfig](c, value)
}

func (_ FfiConverterOptionalNodeDiscoveryConfig) Write(writer io.Writer, value *NodeDiscoveryConfig) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterNodeDiscoveryConfigINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalNodeDiscoveryConfig struct{}

func (_ FfiDestroyerOptionalNodeDiscoveryConfig) Destroy(value *NodeDiscoveryConfig) {
	if value != nil {
		FfiDestroyerNodeDiscoveryConfig{}.Destroy(*value)
	}
}

type FfiConverterOptionalSequenceBytes struct{}

var FfiConverterOptionalSequenceBytesINSTANCE = FfiConverterOptionalSequenceBytes{}

func (c FfiConverterOptionalSequenceBytes) Lift(rb RustBufferI) *[][]byte {
	return LiftFromRustBuffer[*[][]byte](c, rb)
}

func (_ FfiConverterOptionalSequenceBytes) Read(reader io.Reader) *[][]byte {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterSequenceBytesINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalSequenceBytes) Lower(value *[][]byte) C.RustBuffer {
	return LowerIntoRustBuffer[*[][]byte](c, value)
}

func (_ FfiConverterOptionalSequenceBytes) Write(writer io.Writer, value *[][]byte) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterSequenceBytesINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalSequenceBytes struct{}

func (_ FfiDestroyerOptionalSequenceBytes) Destroy(value *[][]byte) {
	if value != nil {
		FfiDestroyerSequenceBytes{}.Destroy(*value)
	}
}

type FfiConverterOptionalMapBytesProtocolCreator struct{}

var FfiConverterOptionalMapBytesProtocolCreatorINSTANCE = FfiConverterOptionalMapBytesProtocolCreator{}

func (c FfiConverterOptionalMapBytesProtocolCreator) Lift(rb RustBufferI) *map[string]ProtocolCreator {
	return LiftFromRustBuffer[*map[string]ProtocolCreator](c, rb)
}

func (_ FfiConverterOptionalMapBytesProtocolCreator) Read(reader io.Reader) *map[string]ProtocolCreator {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterMapBytesProtocolCreatorINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalMapBytesProtocolCreator) Lower(value *map[string]ProtocolCreator) C.RustBuffer {
	return LowerIntoRustBuffer[*map[string]ProtocolCreator](c, value)
}

func (_ FfiConverterOptionalMapBytesProtocolCreator) Write(writer io.Writer, value *map[string]ProtocolCreator) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterMapBytesProtocolCreatorINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalMapBytesProtocolCreator struct{}

func (_ FfiDestroyerOptionalMapBytesProtocolCreator) Destroy(value *map[string]ProtocolCreator) {
	if value != nil {
		FfiDestroyerMapBytesProtocolCreator{}.Destroy(*value)
	}
}

type FfiConverterSequenceString struct{}

var FfiConverterSequenceStringINSTANCE = FfiConverterSequenceString{}

func (c FfiConverterSequenceString) Lift(rb RustBufferI) []string {
	return LiftFromRustBuffer[[]string](c, rb)
}

func (c FfiConverterSequenceString) Read(reader io.Reader) []string {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]string, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterStringINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceString) Lower(value []string) C.RustBuffer {
	return LowerIntoRustBuffer[[]string](c, value)
}

func (c FfiConverterSequenceString) Write(writer io.Writer, value []string) {
	if len(value) > math.MaxInt32 {
		panic("[]string is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterStringINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceString struct{}

func (FfiDestroyerSequenceString) Destroy(sequence []string) {
	for _, value := range sequence {
		FfiDestroyerString{}.Destroy(value)
	}
}

type FfiConverterSequenceBytes struct{}

var FfiConverterSequenceBytesINSTANCE = FfiConverterSequenceBytes{}

func (c FfiConverterSequenceBytes) Lift(rb RustBufferI) [][]byte {
	return LiftFromRustBuffer[[][]byte](c, rb)
}

func (c FfiConverterSequenceBytes) Read(reader io.Reader) [][]byte {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([][]byte, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterBytesINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceBytes) Lower(value [][]byte) C.RustBuffer {
	return LowerIntoRustBuffer[[][]byte](c, value)
}

func (c FfiConverterSequenceBytes) Write(writer io.Writer, value [][]byte) {
	if len(value) > math.MaxInt32 {
		panic("[][]byte is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterBytesINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceBytes struct{}

func (FfiDestroyerSequenceBytes) Destroy(sequence [][]byte) {
	for _, value := range sequence {
		FfiDestroyerBytes{}.Destroy(value)
	}
}

type FfiConverterSequenceAuthorId struct{}

var FfiConverterSequenceAuthorIdINSTANCE = FfiConverterSequenceAuthorId{}

func (c FfiConverterSequenceAuthorId) Lift(rb RustBufferI) []*AuthorId {
	return LiftFromRustBuffer[[]*AuthorId](c, rb)
}

func (c FfiConverterSequenceAuthorId) Read(reader io.Reader) []*AuthorId {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]*AuthorId, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterAuthorIdINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceAuthorId) Lower(value []*AuthorId) C.RustBuffer {
	return LowerIntoRustBuffer[[]*AuthorId](c, value)
}

func (c FfiConverterSequenceAuthorId) Write(writer io.Writer, value []*AuthorId) {
	if len(value) > math.MaxInt32 {
		panic("[]*AuthorId is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterAuthorIdINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceAuthorId struct{}

func (FfiDestroyerSequenceAuthorId) Destroy(sequence []*AuthorId) {
	for _, value := range sequence {
		FfiDestroyerAuthorId{}.Destroy(value)
	}
}

type FfiConverterSequenceDirectAddrInfo struct{}

var FfiConverterSequenceDirectAddrInfoINSTANCE = FfiConverterSequenceDirectAddrInfo{}

func (c FfiConverterSequenceDirectAddrInfo) Lift(rb RustBufferI) []*DirectAddrInfo {
	return LiftFromRustBuffer[[]*DirectAddrInfo](c, rb)
}

func (c FfiConverterSequenceDirectAddrInfo) Read(reader io.Reader) []*DirectAddrInfo {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]*DirectAddrInfo, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterDirectAddrInfoINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceDirectAddrInfo) Lower(value []*DirectAddrInfo) C.RustBuffer {
	return LowerIntoRustBuffer[[]*DirectAddrInfo](c, value)
}

func (c FfiConverterSequenceDirectAddrInfo) Write(writer io.Writer, value []*DirectAddrInfo) {
	if len(value) > math.MaxInt32 {
		panic("[]*DirectAddrInfo is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterDirectAddrInfoINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceDirectAddrInfo struct{}

func (FfiDestroyerSequenceDirectAddrInfo) Destroy(sequence []*DirectAddrInfo) {
	for _, value := range sequence {
		FfiDestroyerDirectAddrInfo{}.Destroy(value)
	}
}

type FfiConverterSequenceEntry struct{}

var FfiConverterSequenceEntryINSTANCE = FfiConverterSequenceEntry{}

func (c FfiConverterSequenceEntry) Lift(rb RustBufferI) []*Entry {
	return LiftFromRustBuffer[[]*Entry](c, rb)
}

func (c FfiConverterSequenceEntry) Read(reader io.Reader) []*Entry {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]*Entry, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterEntryINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceEntry) Lower(value []*Entry) C.RustBuffer {
	return LowerIntoRustBuffer[[]*Entry](c, value)
}

func (c FfiConverterSequenceEntry) Write(writer io.Writer, value []*Entry) {
	if len(value) > math.MaxInt32 {
		panic("[]*Entry is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterEntryINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceEntry struct{}

func (FfiDestroyerSequenceEntry) Destroy(sequence []*Entry) {
	for _, value := range sequence {
		FfiDestroyerEntry{}.Destroy(value)
	}
}

type FfiConverterSequenceFilterKind struct{}

var FfiConverterSequenceFilterKindINSTANCE = FfiConverterSequenceFilterKind{}

func (c FfiConverterSequenceFilterKind) Lift(rb RustBufferI) []*FilterKind {
	return LiftFromRustBuffer[[]*FilterKind](c, rb)
}

func (c FfiConverterSequenceFilterKind) Read(reader io.Reader) []*FilterKind {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]*FilterKind, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterFilterKindINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceFilterKind) Lower(value []*FilterKind) C.RustBuffer {
	return LowerIntoRustBuffer[[]*FilterKind](c, value)
}

func (c FfiConverterSequenceFilterKind) Write(writer io.Writer, value []*FilterKind) {
	if len(value) > math.MaxInt32 {
		panic("[]*FilterKind is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterFilterKindINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceFilterKind struct{}

func (FfiDestroyerSequenceFilterKind) Destroy(sequence []*FilterKind) {
	for _, value := range sequence {
		FfiDestroyerFilterKind{}.Destroy(value)
	}
}

type FfiConverterSequenceHash struct{}

var FfiConverterSequenceHashINSTANCE = FfiConverterSequenceHash{}

func (c FfiConverterSequenceHash) Lift(rb RustBufferI) []*Hash {
	return LiftFromRustBuffer[[]*Hash](c, rb)
}

func (c FfiConverterSequenceHash) Read(reader io.Reader) []*Hash {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]*Hash, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterHashINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceHash) Lower(value []*Hash) C.RustBuffer {
	return LowerIntoRustBuffer[[]*Hash](c, value)
}

func (c FfiConverterSequenceHash) Write(writer io.Writer, value []*Hash) {
	if len(value) > math.MaxInt32 {
		panic("[]*Hash is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterHashINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceHash struct{}

func (FfiDestroyerSequenceHash) Destroy(sequence []*Hash) {
	for _, value := range sequence {
		FfiDestroyerHash{}.Destroy(value)
	}
}

type FfiConverterSequenceNodeAddr struct{}

var FfiConverterSequenceNodeAddrINSTANCE = FfiConverterSequenceNodeAddr{}

func (c FfiConverterSequenceNodeAddr) Lift(rb RustBufferI) []*NodeAddr {
	return LiftFromRustBuffer[[]*NodeAddr](c, rb)
}

func (c FfiConverterSequenceNodeAddr) Read(reader io.Reader) []*NodeAddr {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]*NodeAddr, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterNodeAddrINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceNodeAddr) Lower(value []*NodeAddr) C.RustBuffer {
	return LowerIntoRustBuffer[[]*NodeAddr](c, value)
}

func (c FfiConverterSequenceNodeAddr) Write(writer io.Writer, value []*NodeAddr) {
	if len(value) > math.MaxInt32 {
		panic("[]*NodeAddr is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterNodeAddrINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceNodeAddr struct{}

func (FfiDestroyerSequenceNodeAddr) Destroy(sequence []*NodeAddr) {
	for _, value := range sequence {
		FfiDestroyerNodeAddr{}.Destroy(value)
	}
}

type FfiConverterSequenceCollectionInfo struct{}

var FfiConverterSequenceCollectionInfoINSTANCE = FfiConverterSequenceCollectionInfo{}

func (c FfiConverterSequenceCollectionInfo) Lift(rb RustBufferI) []CollectionInfo {
	return LiftFromRustBuffer[[]CollectionInfo](c, rb)
}

func (c FfiConverterSequenceCollectionInfo) Read(reader io.Reader) []CollectionInfo {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]CollectionInfo, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterCollectionInfoINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceCollectionInfo) Lower(value []CollectionInfo) C.RustBuffer {
	return LowerIntoRustBuffer[[]CollectionInfo](c, value)
}

func (c FfiConverterSequenceCollectionInfo) Write(writer io.Writer, value []CollectionInfo) {
	if len(value) > math.MaxInt32 {
		panic("[]CollectionInfo is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterCollectionInfoINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceCollectionInfo struct{}

func (FfiDestroyerSequenceCollectionInfo) Destroy(sequence []CollectionInfo) {
	for _, value := range sequence {
		FfiDestroyerCollectionInfo{}.Destroy(value)
	}
}

type FfiConverterSequenceIncompleteBlobInfo struct{}

var FfiConverterSequenceIncompleteBlobInfoINSTANCE = FfiConverterSequenceIncompleteBlobInfo{}

func (c FfiConverterSequenceIncompleteBlobInfo) Lift(rb RustBufferI) []IncompleteBlobInfo {
	return LiftFromRustBuffer[[]IncompleteBlobInfo](c, rb)
}

func (c FfiConverterSequenceIncompleteBlobInfo) Read(reader io.Reader) []IncompleteBlobInfo {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]IncompleteBlobInfo, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterIncompleteBlobInfoINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceIncompleteBlobInfo) Lower(value []IncompleteBlobInfo) C.RustBuffer {
	return LowerIntoRustBuffer[[]IncompleteBlobInfo](c, value)
}

func (c FfiConverterSequenceIncompleteBlobInfo) Write(writer io.Writer, value []IncompleteBlobInfo) {
	if len(value) > math.MaxInt32 {
		panic("[]IncompleteBlobInfo is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterIncompleteBlobInfoINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceIncompleteBlobInfo struct{}

func (FfiDestroyerSequenceIncompleteBlobInfo) Destroy(sequence []IncompleteBlobInfo) {
	for _, value := range sequence {
		FfiDestroyerIncompleteBlobInfo{}.Destroy(value)
	}
}

type FfiConverterSequenceLinkAndName struct{}

var FfiConverterSequenceLinkAndNameINSTANCE = FfiConverterSequenceLinkAndName{}

func (c FfiConverterSequenceLinkAndName) Lift(rb RustBufferI) []LinkAndName {
	return LiftFromRustBuffer[[]LinkAndName](c, rb)
}

func (c FfiConverterSequenceLinkAndName) Read(reader io.Reader) []LinkAndName {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]LinkAndName, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterLinkAndNameINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceLinkAndName) Lower(value []LinkAndName) C.RustBuffer {
	return LowerIntoRustBuffer[[]LinkAndName](c, value)
}

func (c FfiConverterSequenceLinkAndName) Write(writer io.Writer, value []LinkAndName) {
	if len(value) > math.MaxInt32 {
		panic("[]LinkAndName is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterLinkAndNameINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceLinkAndName struct{}

func (FfiDestroyerSequenceLinkAndName) Destroy(sequence []LinkAndName) {
	for _, value := range sequence {
		FfiDestroyerLinkAndName{}.Destroy(value)
	}
}

type FfiConverterSequenceNamespaceAndCapability struct{}

var FfiConverterSequenceNamespaceAndCapabilityINSTANCE = FfiConverterSequenceNamespaceAndCapability{}

func (c FfiConverterSequenceNamespaceAndCapability) Lift(rb RustBufferI) []NamespaceAndCapability {
	return LiftFromRustBuffer[[]NamespaceAndCapability](c, rb)
}

func (c FfiConverterSequenceNamespaceAndCapability) Read(reader io.Reader) []NamespaceAndCapability {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]NamespaceAndCapability, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterNamespaceAndCapabilityINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceNamespaceAndCapability) Lower(value []NamespaceAndCapability) C.RustBuffer {
	return LowerIntoRustBuffer[[]NamespaceAndCapability](c, value)
}

func (c FfiConverterSequenceNamespaceAndCapability) Write(writer io.Writer, value []NamespaceAndCapability) {
	if len(value) > math.MaxInt32 {
		panic("[]NamespaceAndCapability is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterNamespaceAndCapabilityINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceNamespaceAndCapability struct{}

func (FfiDestroyerSequenceNamespaceAndCapability) Destroy(sequence []NamespaceAndCapability) {
	for _, value := range sequence {
		FfiDestroyerNamespaceAndCapability{}.Destroy(value)
	}
}

type FfiConverterSequenceRemoteInfo struct{}

var FfiConverterSequenceRemoteInfoINSTANCE = FfiConverterSequenceRemoteInfo{}

func (c FfiConverterSequenceRemoteInfo) Lift(rb RustBufferI) []RemoteInfo {
	return LiftFromRustBuffer[[]RemoteInfo](c, rb)
}

func (c FfiConverterSequenceRemoteInfo) Read(reader io.Reader) []RemoteInfo {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]RemoteInfo, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterRemoteInfoINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceRemoteInfo) Lower(value []RemoteInfo) C.RustBuffer {
	return LowerIntoRustBuffer[[]RemoteInfo](c, value)
}

func (c FfiConverterSequenceRemoteInfo) Write(writer io.Writer, value []RemoteInfo) {
	if len(value) > math.MaxInt32 {
		panic("[]RemoteInfo is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterRemoteInfoINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceRemoteInfo struct{}

func (FfiDestroyerSequenceRemoteInfo) Destroy(sequence []RemoteInfo) {
	for _, value := range sequence {
		FfiDestroyerRemoteInfo{}.Destroy(value)
	}
}

type FfiConverterSequenceTagInfo struct{}

var FfiConverterSequenceTagInfoINSTANCE = FfiConverterSequenceTagInfo{}

func (c FfiConverterSequenceTagInfo) Lift(rb RustBufferI) []TagInfo {
	return LiftFromRustBuffer[[]TagInfo](c, rb)
}

func (c FfiConverterSequenceTagInfo) Read(reader io.Reader) []TagInfo {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]TagInfo, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterTagInfoINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceTagInfo) Lower(value []TagInfo) C.RustBuffer {
	return LowerIntoRustBuffer[[]TagInfo](c, value)
}

func (c FfiConverterSequenceTagInfo) Write(writer io.Writer, value []TagInfo) {
	if len(value) > math.MaxInt32 {
		panic("[]TagInfo is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterTagInfoINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceTagInfo struct{}

func (FfiDestroyerSequenceTagInfo) Destroy(sequence []TagInfo) {
	for _, value := range sequence {
		FfiDestroyerTagInfo{}.Destroy(value)
	}
}

type FfiConverterMapStringCounterStats struct{}

var FfiConverterMapStringCounterStatsINSTANCE = FfiConverterMapStringCounterStats{}

func (c FfiConverterMapStringCounterStats) Lift(rb RustBufferI) map[string]CounterStats {
	return LiftFromRustBuffer[map[string]CounterStats](c, rb)
}

func (_ FfiConverterMapStringCounterStats) Read(reader io.Reader) map[string]CounterStats {
	result := make(map[string]CounterStats)
	length := readInt32(reader)
	for i := int32(0); i < length; i++ {
		key := FfiConverterStringINSTANCE.Read(reader)
		value := FfiConverterCounterStatsINSTANCE.Read(reader)
		result[key] = value
	}
	return result
}

func (c FfiConverterMapStringCounterStats) Lower(value map[string]CounterStats) C.RustBuffer {
	return LowerIntoRustBuffer[map[string]CounterStats](c, value)
}

func (_ FfiConverterMapStringCounterStats) Write(writer io.Writer, mapValue map[string]CounterStats) {
	if len(mapValue) > math.MaxInt32 {
		panic("map[string]CounterStats is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(mapValue)))
	for key, value := range mapValue {
		FfiConverterStringINSTANCE.Write(writer, key)
		FfiConverterCounterStatsINSTANCE.Write(writer, value)
	}
}

type FfiDestroyerMapStringCounterStats struct{}

func (_ FfiDestroyerMapStringCounterStats) Destroy(mapValue map[string]CounterStats) {
	for key, value := range mapValue {
		FfiDestroyerString{}.Destroy(key)
		FfiDestroyerCounterStats{}.Destroy(value)
	}
}

type FfiConverterMapBytesProtocolCreator struct{}

var FfiConverterMapBytesProtocolCreatorINSTANCE = FfiConverterMapBytesProtocolCreator{}

func (c FfiConverterMapBytesProtocolCreator) Lift(rb RustBufferI) map[string]ProtocolCreator {
	return LiftFromRustBuffer[map[string]ProtocolCreator](c, rb)
}

func (_ FfiConverterMapBytesProtocolCreator) Read(reader io.Reader) map[string]ProtocolCreator {
	result := make(map[string]ProtocolCreator)
	length := readInt32(reader)
	for i := int32(0); i < length; i++ {
		key := FfiConverterBytesINSTANCE.Read(reader)
		value := FfiConverterProtocolCreatorINSTANCE.Read(reader)
		result[string(key)] = value
	}
	return result
}

func (c FfiConverterMapBytesProtocolCreator) Lower(value map[string]ProtocolCreator) C.RustBuffer {
	return LowerIntoRustBuffer[map[string]ProtocolCreator](c, value)
}

func (_ FfiConverterMapBytesProtocolCreator) Write(writer io.Writer, mapValue map[string]ProtocolCreator) {
	if len(mapValue) > math.MaxInt32 {
		panic("map[string]ProtocolCreator is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(mapValue)))
	for key, value := range mapValue {
		FfiConverterBytesINSTANCE.Write(writer, []byte(key))
		FfiConverterProtocolCreatorINSTANCE.Write(writer, value)
	}
}

type FfiDestroyerMapBytesProtocolCreator struct{}

func (_ FfiDestroyerMapBytesProtocolCreator) Destroy(mapValue map[string]ProtocolCreator) {
	for key, value := range mapValue {
		FfiDestroyerBytes{}.Destroy([]byte(key))
		FfiDestroyerProtocolCreator{}.Destroy(value)
	}
}

const (
	uniffiRustFuturePollReady      int8 = 0
	uniffiRustFuturePollMaybeReady int8 = 1
)

type rustFuturePollFunc func(C.uint64_t, C.UniffiRustFutureContinuationCallback, C.uint64_t)
type rustFutureCompleteFunc[T any] func(C.uint64_t, *C.RustCallStatus) T
type rustFutureFreeFunc func(C.uint64_t)

//export iroh_ffi_uniffiFutureContinuationCallback
func iroh_ffi_uniffiFutureContinuationCallback(data C.uint64_t, pollResult C.int8_t) {
	h := cgo.Handle(uintptr(data))
	waiter := h.Value().(chan int8)
	waiter <- int8(pollResult)
}

func uniffiRustCallAsync[E any, T any, F any](
	errConverter BufReader[*E],
	completeFunc rustFutureCompleteFunc[F],
	liftFunc func(F) T,
	rustFuture C.uint64_t,
	pollFunc rustFuturePollFunc,
	freeFunc rustFutureFreeFunc,
) (T, *E) {
	defer freeFunc(rustFuture)

	pollResult := int8(-1)
	waiter := make(chan int8, 1)

	chanHandle := cgo.NewHandle(waiter)
	defer chanHandle.Delete()

	for pollResult != uniffiRustFuturePollReady {
		pollFunc(
			rustFuture,
			(C.UniffiRustFutureContinuationCallback)(C.iroh_ffi_uniffiFutureContinuationCallback),
			C.uint64_t(chanHandle),
		)
		pollResult = <-waiter
	}

	var goValue T
	var ffiValue F
	var err *E

	ffiValue, err = rustCallWithError(errConverter, func(status *C.RustCallStatus) F {
		return completeFunc(rustFuture, status)
	})
	if err != nil {
		return goValue, err
	}
	return liftFunc(ffiValue), nil
}

//export iroh_ffi_uniffiFreeGorutine
func iroh_ffi_uniffiFreeGorutine(data C.uint64_t) {
	handle := cgo.Handle(uintptr(data))
	defer handle.Delete()

	guard := handle.Value().(chan struct{})
	guard <- struct{}{}
}

// Helper function that translates a key that was derived from the [`path_to_key`] function back
// into a path.
//
// If `prefix` exists, it will be stripped before converting back to a path
// If `root` exists, will add the root as a parent to the created path
// Removes any null byte that has been appened to the key
func KeyToPath(key []byte, prefix *string, root *string) (string, *IrohError) {
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_func_key_to_path(FfiConverterBytesINSTANCE.Lower(key), FfiConverterOptionalStringINSTANCE.Lower(prefix), FfiConverterOptionalStringINSTANCE.Lower(root), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Helper function that creates a document key from a canonicalized path, removing the `root` and adding the `prefix`, if they exist
//
// Appends the null byte to the end of the key.
func PathToKey(path string, prefix *string, root *string) ([]byte, *IrohError) {
	_uniffiRV, _uniffiErr := rustCallWithError[IrohError](FfiConverterIrohError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_iroh_ffi_fn_func_path_to_key(FfiConverterStringINSTANCE.Lower(path), FfiConverterOptionalStringINSTANCE.Lower(prefix), FfiConverterOptionalStringINSTANCE.Lower(root), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue []byte
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBytesINSTANCE.Lift(_uniffiRV), _uniffiErr
	}
}

// Set the logging level.
func SetLogLevel(level LogLevel) {
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_iroh_ffi_fn_func_set_log_level(FfiConverterLogLevelINSTANCE.Lower(level), _uniffiStatus)
		return false
	})
}
