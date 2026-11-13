// Copyright 2026, Asterisk4Magisk contributors
// SPDX-License-Identifier: LGPL-3.0

#ifndef ASTERISK_OWNER_DIAG_H
#define ASTERISK_OWNER_DIAG_H

#include <arpa/inet.h>
#include <errno.h>
#include <linux/inet_diag.h>
#include <linux/netlink.h>
#include <linux/sock_diag.h>
#include <stdint.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <unistd.h>

// One caller owns the query until the complete response has been consumed.
// Keep the socket and receive storage across flows, without caching ownership.
struct flow_owner_query {
    int fd_plus_one;
    uint32_t sequence;
    union {
        struct nlmsghdr alignment;
        unsigned char bytes[16384];
    } buffer;
};

static void flow_owner_close(struct flow_owner_query *q) {
    if (q->fd_plus_one) close(q->fd_plus_one - 1);
    q->fd_plus_one = 0;
}

static int flow_owner_open(struct flow_owner_query *q) {
    if (q->fd_plus_one) return 0;
    int fd = socket(AF_NETLINK, SOCK_DGRAM | SOCK_CLOEXEC, NETLINK_SOCK_DIAG);
    if (fd < 0) return -errno;
    struct timeval timeout = {.tv_usec = 100000};
    struct sockaddr_nl address = {.nl_family = AF_NETLINK};
    if (setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof(timeout)) ||
        setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &timeout, sizeof(timeout)) ||
        connect(fd, (void *)&address, sizeof(address))) {
        int error = errno;
        close(fd);
        return -error;
    }
    q->fd_plus_one = fd + 1;
    return 0;
}

static int flow_owner_is_v4(const unsigned char *ip) {
    static const unsigned char prefix[12] = {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255, 255};
    return !memcmp(ip, prefix, sizeof(prefix));
}

static int flow_owner_address_matches(int family, const void *raw, const unsigned char *ip, int wildcard) {
    const unsigned char *address = raw;
    static const unsigned char zero[16] = {0};
    size_t width = family == AF_INET ? 4 : 16;
    if (wildcard && !memcmp(address, zero, width)) return 1;
    if (family == AF_INET) return flow_owner_is_v4(ip) && !memcmp(address, ip + 12, 4);
    return !memcmp(address, ip, 16);
}

// Returns -EEXIST for conflicting UIDs, including conflicts across IPv4/IPv6.
static int flow_owner_candidate(const struct inet_diag_msg *entry, int udp,
    const unsigned char *source, uint16_t source_port, const unsigned char *destination,
    uint16_t destination_port, int *found, uint32_t *uid, uint32_t *inode) {
    static const unsigned char zero[16] = {0};
    int family = entry->idiag_family;
    if (family != AF_INET && family != AF_INET6) return 0;
    if (!entry->idiag_inode && !entry->idiag_uid) return 0;
    if (entry->id.idiag_sport != htons(source_port) ||
        !flow_owner_address_matches(family, entry->id.idiag_src, source, udp)) return 0;
    int unconnected = udp && !entry->id.idiag_dport &&
        !memcmp(entry->id.idiag_dst, zero, family == AF_INET ? 4 : 16);
    if (destination_port && !unconnected && (entry->id.idiag_dport != htons(destination_port) ||
        !flow_owner_address_matches(family, entry->id.idiag_dst, destination, 0))) return 0;
    if (*found && *uid != entry->idiag_uid) return -EEXIST;
    *found = 1;
    *uid = entry->idiag_uid;
    *inode = entry->idiag_inode;
    return 0;
}

static int flow_owner_request(struct flow_owner_query *q, int family, int protocol, int dump,
    const unsigned char *source, uint16_t source_port, const unsigned char *destination,
    uint16_t destination_port, int *found, uint32_t *uid, uint32_t *inode) {
    int error = flow_owner_open(q);
    if (error) return error;
    struct {
        struct nlmsghdr header;
        struct inet_diag_req_v2 request;
    } message;
    memset(&message, 0, sizeof(message));
    message.header.nlmsg_len = sizeof(message);
    message.header.nlmsg_type = SOCK_DIAG_BY_FAMILY;
    message.header.nlmsg_flags = NLM_F_REQUEST | (dump ? NLM_F_DUMP : 0);
    message.header.nlmsg_seq = ++q->sequence;
    message.request.sdiag_family = family;
    message.request.sdiag_protocol = protocol;
    message.request.idiag_states = dump ? UINT32_MAX : 0;
    message.request.id.idiag_sport = htons(source_port);
    memcpy(message.request.id.idiag_src, source + (family == AF_INET ? 12 : 0), family == AF_INET ? 4 : 16);
    if (!dump) {
        message.request.id.idiag_dport = htons(destination_port);
        memcpy(message.request.id.idiag_dst, destination + (family == AF_INET ? 12 : 0), family == AF_INET ? 4 : 16);
    }
    message.request.id.idiag_cookie[0] = message.request.id.idiag_cookie[1] = UINT32_MAX;
    if (send(q->fd_plus_one - 1, &message, sizeof(message), 0) != sizeof(message)) {
        error = errno ? -errno : -EIO;
        flow_owner_close(q);
        return error;
    }
    for (;;) {
        struct iovec iov = {.iov_base = q->buffer.bytes, .iov_len = sizeof(q->buffer.bytes)};
        struct msghdr response = {.msg_iov = &iov, .msg_iovlen = 1};
        ssize_t length = recvmsg(q->fd_plus_one - 1, &response, 0);
        if (length <= 0 || (response.msg_flags & MSG_TRUNC)) {
            error = length < 0 ? -errno : -EIO;
            flow_owner_close(q);
            return error;
        }
        int remaining = (int)length;
        for (struct nlmsghdr *header = (void *)q->buffer.bytes;
             NLMSG_OK(header, remaining); header = NLMSG_NEXT(header, remaining)) {
            if (header->nlmsg_seq != q->sequence) continue;
            if (header->nlmsg_flags & NLM_F_DUMP_INTR) {
                flow_owner_close(q);
                return -EAGAIN;
            }
            if (header->nlmsg_type == NLMSG_DONE) {
                if (NLMSG_PAYLOAD(header, 0) >= sizeof(int)) {
                    memcpy(&error, NLMSG_DATA(header), sizeof(error));
                    if (error) { flow_owner_close(q); return error; }
                }
                return 0;
            }
            if (header->nlmsg_type == NLMSG_ERROR) {
                if (NLMSG_PAYLOAD(header, 0) < sizeof(struct nlmsgerr)) {
                    flow_owner_close(q);
                    return -EIO;
                }
                return ((struct nlmsgerr *)NLMSG_DATA(header))->error;
            }
            if (header->nlmsg_type != SOCK_DIAG_BY_FAMILY ||
                NLMSG_PAYLOAD(header, 0) < sizeof(struct inet_diag_msg)) continue;
            error = flow_owner_candidate(NLMSG_DATA(header), protocol == IPPROTO_UDP,
                source, source_port, destination, destination_port, found, uid, inode);
            if (error) { flow_owner_close(q); return error; }
            if (!dump) return *found ? 0 : -ENOENT;
        }
        if (remaining) { flow_owner_close(q); return -EIO; }
    }
}

static int flow_owner_lookup(struct flow_owner_query *q, int udp,
    const unsigned char *source, uint16_t source_port, const unsigned char *destination,
    uint16_t destination_port, uint32_t *uid, uint32_t *inode) {
    int found = 0, error = -ENOENT;
    int family = flow_owner_is_v4(source) ? AF_INET : AF_INET6;
    int protocol = udp ? IPPROTO_UDP : IPPROTO_TCP;
    // A UDP exact lookup selects one SO_REUSEPORT member. Enumerate the
    // requested local port instead, retaining the complete tuple and ambiguity
    // check. Filtering happens before the kernel emits binary records.
    if (!udp && destination_port) {
        error = flow_owner_request(q, family, protocol, 0, source, source_port,
            destination, destination_port, &found, uid, inode);
        if (!error && found) return 0;
        if (error != -ENOENT && error != -ESRCH) return error;
    }
    error = flow_owner_request(q, family, protocol, 1, source, source_port,
        destination, destination_port, &found, uid, inode);
    if (error) return error;
    if (family == AF_INET) {
        error = flow_owner_request(q, AF_INET6, protocol, 1, source, source_port,
            destination, destination_port, &found, uid, inode);
        if (error && error != -EAFNOSUPPORT && error != -EPROTONOSUPPORT) return error;
    }
    return found ? 0 : -ENOENT;
}

#endif
