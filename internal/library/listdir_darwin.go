//go:build darwin && cgo

package library

/*
#include <sys/attr.h>
#include <sys/vnode.h>
#include <unistd.h>
#include <string.h>
#include <stdint.h>

typedef struct {
	char name[256];
	uint32_t objtype;
	int64_t mtime_sec;
	int64_t mtime_nsec;
	int64_t size;
} bulk_entry;

// bulk_read fills out with the next batch of entries from the open
// directory fd. Returns how many, 0 at the end, -1 on error.
static int bulk_read(int fd, bulk_entry *out, int max) {
	struct attrlist al;
	memset(&al, 0, sizeof al);
	al.bitmapcount = ATTR_BIT_MAP_COUNT;
	al.commonattr = ATTR_CMN_RETURNED_ATTRS | ATTR_CMN_NAME | ATTR_CMN_OBJTYPE | ATTR_CMN_MODTIME;
	al.fileattr = ATTR_FILE_DATALENGTH;
	static char buf[512 * 1024];
	int n = getattrlistbulk(fd, &al, buf, sizeof buf, FSOPT_PACK_INVAL_ATTRS);
	if (n <= 0) return n;
	if (n > max) n = max;
	char *p = buf;
	for (int i = 0; i < n; i++) {
		uint32_t len;
		memcpy(&len, p, sizeof len);
		char *e = p + sizeof len;
		attribute_set_t ret;
		memcpy(&ret, e, sizeof ret);
		e += sizeof ret;
		bulk_entry *o = &out[i];
		o->name[0] = 0;
		o->objtype = 0;
		o->size = 0;
		o->mtime_sec = 0;
		o->mtime_nsec = 0;
		if (ret.commonattr & ATTR_CMN_NAME) {
			attrreference_t r;
			memcpy(&r, e, sizeof r);
			size_t l = r.attr_length; // includes the NUL
			if (l > 0) l--;
			if (l >= sizeof o->name) l = sizeof o->name - 1;
			memcpy(o->name, e + r.attr_dataoffset, l);
			o->name[l] = 0;
			e += sizeof r;
		}
		if (ret.commonattr & ATTR_CMN_OBJTYPE) {
			fsobj_type_t t;
			memcpy(&t, e, sizeof t);
			o->objtype = t;
			e += sizeof t;
		}
		if (ret.commonattr & ATTR_CMN_MODTIME) {
			struct timespec ts;
			memcpy(&ts, e, sizeof ts);
			o->mtime_sec = ts.tv_sec;
			o->mtime_nsec = ts.tv_nsec;
			e += sizeof ts;
		}
		if (ret.fileattr & ATTR_FILE_DATALENGTH) {
			off_t sz;
			memcpy(&sz, e, sizeof sz);
			o->size = sz;
			e += sizeof sz;
		}
		p += len;
	}
	return n;
}
*/
import "C"

import (
	"os"
	"sync"
	"time"

	"github.com/rotisserie/eris"
	"golang.org/x/sys/unix"
)

// listDir reads a folder with getattrlistbulk, which asks the file system
// for the names, sizes and dates of a whole directory in a few calls. On an
// SMB share that is the difference between a listing and a stat round-trip
// per file — several times faster, and what Finder does.
//
// The C shim keeps one static buffer, so only one listing runs at a time.
var bulkMu sync.Mutex

func listDir(dir string) ([]dirEntry, error) {
	bulkMu.Lock()
	defer bulkMu.Unlock()
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, eris.Wrapf(err, "open %s", dir)
	}
	defer unix.Close(fd)

	const batch = 4096
	buf := make([]C.bulk_entry, batch)
	var out []dirEntry
	for {
		n, err := C.bulk_read(C.int(fd), &buf[0], C.int(batch))
		if n < 0 {
			if err == nil {
				err = os.ErrInvalid
			}
			return nil, eris.Wrapf(err, "list %s", dir)
		}
		if n == 0 {
			return out, nil
		}
		for i := 0; i < int(n); i++ {
			e := &buf[i]
			name := C.GoString(&e.name[0])
			if name == "" || name == "." || name == ".." {
				continue
			}
			switch e.objtype {
			case C.VDIR:
				out = append(out, dirEntry{name: name, isDir: true})
			case C.VREG:
				out = append(out, dirEntry{
					name: name, size: int64(e.size),
					mod: time.Unix(int64(e.mtime_sec), int64(e.mtime_nsec)),
				})
			}
		}
	}
}
