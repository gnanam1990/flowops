package ascpring6

import (
	"os"
	"reflect"
)

// socketIdentity captures the filesystem generation as well as device/inode.
// Long-lived component pins additionally keep the original inode allocated;
// runtime publication compares the full identity at each transition.
type socketIdentity struct {
	device, inode       uint64
	changeSec, changeNS int64
}

func identityFromFileInfo(info os.FileInfo) (socketIdentity, bool) {
	if info == nil || info.Sys() == nil {
		return socketIdentity{}, false
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return socketIdentity{}, false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return socketIdentity{}, false
	}
	device, ok := unsignedStatField(value, "Dev")
	if !ok {
		return socketIdentity{}, false
	}
	inode, ok := unsignedStatField(value, "Ino")
	if !ok {
		return socketIdentity{}, false
	}
	changed := value.FieldByName("Ctim")
	if !changed.IsValid() {
		changed = value.FieldByName("Ctimespec")
	}
	if !changed.IsValid() || changed.Kind() != reflect.Struct {
		return socketIdentity{}, false
	}
	seconds, ok := signedStatField(changed, "Sec")
	if !ok {
		return socketIdentity{}, false
	}
	nanoseconds, ok := signedStatField(changed, "Nsec")
	if !ok {
		return socketIdentity{}, false
	}
	return socketIdentity{device: device, inode: inode, changeSec: seconds, changeNS: nanoseconds}, true
}

func unsignedStatField(value reflect.Value, name string) (uint64, bool) {
	field := value.FieldByName(name)
	if !field.IsValid() {
		return 0, false
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return field.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if field.Int() < 0 {
			return 0, false
		}
		return uint64(field.Int()), true
	default:
		return 0, false
	}
}

func signedStatField(value reflect.Value, name string) (int64, bool) {
	field := value.FieldByName(name)
	if !field.IsValid() {
		return 0, false
	}
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return field.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return int64(field.Uint()), true
	default:
		return 0, false
	}
}
