// LD_PRELOAD shim for Nickel under qemu-user. /dev/fb0 is a plain file in
// the emulator. The shim answers the framebuffer ioctls for it and accepts
// the e-ink update ioctls. All other ioctls go to the real call.
//
// Adapted from vm/src/vmshim.c of NickelInkSync PR 27
// (github.com/dkuester/NickelInkSync, MIT license).
#define _GNU_SOURCE
#include <dlfcn.h>
#include <fcntl.h>
#include <linux/fb.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <unistd.h>

#define FB_W 1072
#define FB_H 1448
#define FB_BPP 32

static int is_fb(int fd)
{
    char link[64], target[256];
    snprintf(link, sizeof(link), "/proc/self/fd/%d", fd);
    ssize_t n = readlink(link, target, sizeof(target) - 1);
    if (n < 0)
        return 0;
    target[n] = 0;
    const char *fb = getenv("VMSHIM_FB_FILE");
    return strcmp(target, "/dev/fb0") == 0 || (fb && strcmp(target, fb) == 0);
}

static int fb_ioctl(unsigned long req, void *arg)
{
    if (req == FBIOGET_VSCREENINFO) {
        struct fb_var_screeninfo *v = arg;
        memset(v, 0, sizeof(*v));
        v->xres = v->xres_virtual = FB_W;
        v->yres = v->yres_virtual = FB_H;
        v->bits_per_pixel = FB_BPP;
        v->red.offset = 16;
        v->red.length = 8;
        v->green.offset = 8;
        v->green.length = 8;
        v->blue.offset = 0;
        v->blue.length = 8;
        v->transp.offset = 24;
        v->transp.length = 8;
        v->width = 91; // mm
        v->height = 122;
    } else if (req == FBIOGET_FSCREENINFO) {
        struct fb_fix_screeninfo *f = arg;
        memset(f, 0, sizeof(*f));
        strcpy(f->id, "hwtcon");
        f->line_length = FB_W * FB_BPP / 8;
        f->smem_len = f->line_length * FB_H;
        f->type = FB_TYPE_PACKED_PIXELS;
        f->visual = FB_VISUAL_TRUECOLOR;
    }
    // Everything else is an e-ink update or waveform request. The file
    // already holds the pixels, so there is nothing to refresh.
    return 0;
}

// /dev/mmcblk0 and its partitions are plain files in the emulator. Nickel
// 4.x opens them with O_TRUNC to write one sector, which on a block device
// changes nothing but on a file deletes the rest. Drop O_TRUNC and O_CREAT.
static int disk_flags(const char *path, int flags)
{
    if (path && strncmp(path, "/dev/mmcblk", 11) == 0)
        flags &= ~(O_TRUNC | O_CREAT);
    return flags;
}

int open(const char *path, int flags, ...)
{
    static int (*real)(const char *, int, ...);
    if (!real)
        real = dlsym(RTLD_NEXT, "open");
    va_list ap;
    va_start(ap, flags);
    int mode = va_arg(ap, int);
    va_end(ap);
    return real(path, disk_flags(path, flags), mode);
}

int open64(const char *path, int flags, ...)
{
    static int (*real)(const char *, int, ...);
    if (!real)
        real = dlsym(RTLD_NEXT, "open64");
    va_list ap;
    va_start(ap, flags);
    int mode = va_arg(ap, int);
    va_end(ap);
    return real(path, disk_flags(path, flags), mode);
}

int ioctl(int fd, unsigned long req, ...)
{
    static int (*real)(int, unsigned long, ...);
    if (!real)
        real = dlsym(RTLD_NEXT, "ioctl");
    va_list ap;
    va_start(ap, req);
    void *arg = va_arg(ap, void *);
    va_end(ap);
    if (is_fb(fd))
        return fb_ioctl(req, arg);
    return real(fd, req, arg);
}
