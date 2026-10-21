// Copyright 2026, AsteriskBOX contributors
// SPDX-License-Identifier: GPL-3.0
package libbox.systemdns;

import android.annotation.SuppressLint;
import android.content.Context;
import android.net.ConnectivityManager;
import android.net.LinkProperties;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.net.NetworkRequest;
import android.os.Build;
import android.os.Handler;
import android.os.HandlerThread;
import android.os.IBinder;
import android.os.Looper;
import android.os.ParcelFileDescriptor;
import android.os.Process;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.FileOutputStream;
import java.io.IOException;
import java.io.OutputStream;
import java.net.Inet6Address;
import java.net.InetAddress;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicReference;

/** Standalone app_process entry point. No Application, Activity, JNI or DNS queries. */
public final class SystemDnsHelper {
    private final ConnectivityManager connectivity;
    private final HandlerThread worker = new HandlerThread("SystemDnsNetwork");
    private final Set<Network> lostNetworks = new HashSet<>();
    private final Object changed = new Object();
    private byte[] snapshot;
    private long generation;

    private static final class State {
        NetworkCapabilities capabilities;
        LinkProperties properties;
    }

    private SystemDnsHelper(Context context) {
        connectivity = context.getSystemService(ConnectivityManager.class);
    }

    public static void main(String[] args) {
        try {
            if (Process.myUid() != 0 || args.length != 1) {
                throw new IllegalArgumentException("System DNS helper requires root and an inherited socket fd");
            }
            int fd = Integer.parseInt(args[0]);
            if (fd < 3) throw new IllegalArgumentException("Invalid system DNS socket fd");
            ParcelFileDescriptor transport = ParcelFileDescriptor.adoptFd(fd);
            Looper.prepareMainLooper();
            SystemDnsHelper helper = new SystemDnsHelper(systemContext());
            // stdin is the inherited socketpair endpoint. Closing the core's
            // endpoint ends this observer without an app lifecycle callback.
            Thread parent = new Thread(() -> {
                try { while (System.in.read() != -1) { } } catch (IOException ignored) { }
                System.exit(0);
            }, "SystemDnsParent");
            parent.setDaemon(true);
            parent.start();
            try (ParcelFileDescriptor owned = transport) {
                helper.run(new FileOutputStream(owned.getFileDescriptor()));
            }
        } catch (Throwable error) {
            error.printStackTrace(System.err);
            System.exit(1);
        }
    }

    // app_process has no app Context. This intentional root-only bootstrap and
    // the death link below are the only hidden-API boundaries; public SDK calls
    // handle Connectivity Binder.
    @SuppressLint("PrivateApi")
    private static Context systemContext() throws ReflectiveOperationException {
        Class<?> activityThread = Class.forName("android.app.ActivityThread");
        Object thread = activityThread.getMethod("systemMain").invoke(null);
        return (Context) activityThread.getMethod("getSystemContext").invoke(thread);
    }

    // Callbacks live in system_server. After it restarts this process would keep
    // running without them and the core would retain stale DNS, so treat the
    // service's death like any other helper failure.
    @SuppressLint("PrivateApi")
    private static void exitWithConnectivityService() throws Exception {
        IBinder service = (IBinder) Class.forName("android.os.ServiceManager")
                .getMethod("getService", String.class).invoke(null, Context.CONNECTIVITY_SERVICE);
        if (service == null) throw new IllegalStateException("Connectivity service unavailable");
        service.linkToDeath(() -> {
            System.err.println("Connectivity service died; system DNS helper exiting");
            System.exit(1);
        }, 0);
    }

    private void run(OutputStream output) throws Exception {
        exitWithConnectivityService();
        worker.start();
        Handler handler = new Handler(worker.getLooper());
        ConnectivityManager.NetworkCallback callback = new ConnectivityManager.NetworkCallback() {
            @Override public void onAvailable(Network network) { lostNetworks.remove(network); publish(); }
            @Override public void onCapabilitiesChanged(Network network, NetworkCapabilities caps) {
                publish();
            }
            @Override public void onLinkPropertiesChanged(Network network, LinkProperties properties) {
                publish();
            }
            @Override public void onLost(Network network) { lostNetworks.add(network); publish(); }
        };
        NetworkRequest.Builder builder = new NetworkRequest.Builder();
        if (Build.VERSION.SDK_INT >= 30) {
            builder.clearCapabilities();
        } else {
            builder.removeCapability(NetworkCapabilities.NET_CAPABILITY_TRUSTED)
                    .removeCapability(NetworkCapabilities.NET_CAPABILITY_NOT_RESTRICTED);
        }
        NetworkRequest request = builder
                .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN).build();
        connectivity.registerNetworkCallback(request, callback, handler);
        connectivity.registerDefaultNetworkCallback(new ConnectivityManager.NetworkCallback() {
            @Override public void onAvailable(Network network) { publish(); }
            @Override public void onLost(Network network) { publish(); }
            @Override public void onCapabilitiesChanged(Network network, NetworkCapabilities caps) { publish(); }
        }, handler);
        CountDownLatch initialized = new CountDownLatch(1);
        AtomicReference<Throwable> failure = new AtomicReference<>();
        handler.post(() -> {
            try {
                publish();
            } catch (Throwable error) { failure.set(error); }
            finally { initialized.countDown(); }
        });
        initialized.await();
        if (failure.get() != null) throw new IllegalStateException("Cannot read network DNS", failure.get());
        System.out.print("READY 1\n");
        System.out.flush();
        // The owning core drains this socketpair. Callbacks only replace the
        // latest immutable frame; no DNS query passes through Android.
        serve(output, -1);
    }

    private void publish() {
        try {
            Network active = connectivity.getActiveNetwork();
            // Callbacks are invalidations, not a second source of truth. A network
            // found at startup may be lingering/background and never have matched
            // the callback request, so retaining its cached LP can miss its loss.
            List<Network> current = Arrays.asList(connectivity.getAllNetworks());
            lostNetworks.retainAll(current);
            Map<Network, State> networks = new HashMap<>();
            List<Network> eligible = new ArrayList<>();
            for (Network network : current) {
                if (lostNetworks.contains(network)) continue;
                State state = new State();
                state.capabilities = connectivity.getNetworkCapabilities(network);
                state.properties = connectivity.getLinkProperties(network);
                networks.put(network, state);
                if (state.capabilities != null && state.properties != null
                        && state.capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                        && state.capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
                        && !state.properties.getDnsServers().isEmpty()) eligible.add(network);
            }
            eligible.sort((a, b) -> Long.compare(a.getNetworkHandle(), b.getNetworkHandle()));
            boolean hasActive = eligible.contains(active);
            boolean hasValidated = false;
            for (Network network : eligible) {
                hasValidated |= networks.get(network).capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED);
            }
            LinkedHashSet<String> servers = new LinkedHashSet<>();
            for (Network network : eligible) {
                State state = networks.get(network);
                if (hasActive ? !network.equals(active) : hasValidated
                        && !state.capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)) continue;
                for (InetAddress address : state.properties.getDnsServers()) {
                    if (address.isAnyLocalAddress() || address.isLoopbackAddress() || address.isMulticastAddress()) continue;
                    // Preserve Android's scope ID for link-local IPv6 servers.
                    String host = address.getHostAddress();
                    servers.add(address instanceof Inet6Address ? "[" + host + "]:53" : host + ":53");
                    if (servers.size() == 32) break;
                }
                if (servers.size() == 32) break;
            }
            byte[] next = (new JSONObject().put("version", 1)
                    .put("servers", new JSONArray(servers)).toString() + "\n").getBytes(StandardCharsets.UTF_8);
            synchronized (changed) {
                if (!Arrays.equals(snapshot, next)) {
                    snapshot = next;
                    generation++;
                    changed.notifyAll();
                }
            }
        } catch (Throwable error) {
            // Do not leave the core using stale DNS after a failed callback.
            error.printStackTrace(System.err);
            System.exit(1);
        }
    }

    private void serve(OutputStream output, long sent) throws IOException, InterruptedException {
        for (;;) {
            byte[] next;
            synchronized (changed) {
                while (sent == generation) changed.wait();
                next = snapshot;
                sent = generation;
            }
            output.write(next);
            output.flush();
        }
    }
}
