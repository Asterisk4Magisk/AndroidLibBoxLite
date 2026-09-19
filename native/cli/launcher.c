#include <dlfcn.h>
#include <errno.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

typedef int (*cli_entry)(int, char **, int, int);

int main(int argc, char **argv) {
    char sibling[PATH_MAX];
    const char *library_path = getenv("ANDROID_BOX_LIBRARY");
    if (library_path) {
        if (library_path[0] != '/') {
            fprintf(stderr, "ANDROID_BOX_LIBRARY must be an absolute path\n");
            return 64;
        }
    } else {
        ssize_t length = readlink("/proc/self/exe", sibling, sizeof(sibling) - 1);
        if (length < 0 || length >= (ssize_t)sizeof(sibling) - 1) {
            fprintf(stderr, "Cannot resolve executable path\n");
            return 70;
        }
        sibling[length] = '\0';
        char *name = strrchr(sibling, '/');
        if (!name || (size_t)(name - sibling + 1) + sizeof("libbox.so") > sizeof(sibling)) return 70;
        strcpy(name + 1, "libbox.so");
        library_path = sibling;
    }
    /* mobileinit redirects these descriptors asynchronously during Go initialization. */
    int saved_out = dup(STDOUT_FILENO);
    int saved_err = dup(STDERR_FILENO);
    if (saved_out < 0 || saved_err < 0) {
        fprintf(stderr, "Cannot preserve standard streams: %s\n", strerror(errno));
        return 70;
    }
    /* Import libc environment before Box package initializers run. */
    if (setenv("ANDROID_BOX_CLI", "1", 1) != 0) return 70;
    void *library = dlopen(library_path, RTLD_NOW | RTLD_LOCAL);
    if (!library) {
        dprintf(saved_err, "Cannot load Box library %s: %s\n", library_path, dlerror());
        return 70;
    }
    cli_entry entry = (cli_entry)dlsym(library, "AndroidBoxCLI_v1");
    if (!entry) {
        dprintf(saved_err, "Incompatible Box library (missing CLI ABI v1): %s\n", dlerror());
        return 71;
    }
    /* Preserve upstream argv exactly. Do not dlclose a Go runtime. */
    return entry(argc, argv, saved_out, saved_err);
}
