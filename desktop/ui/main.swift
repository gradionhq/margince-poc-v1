// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The Margince window.
//
// This process owns NSApplication so macOS treats the bundle as one app —
// one Dock tile, one menu bar, one Cmd-Q. It supervises the Go launcher as a
// child rather than the other way round: a Go binary has no AppKit run loop,
// so a GUI spawned beneath it would appear to the system as a second,
// unrelated application.
//
// The window's lifetime is the app's lifetime. Quitting stops the stack.

import AppKit
import WebKit

/// Matches the launcher's readyPrefix. Changing either side alone leaves the
/// window waiting forever on a stack that is already up.
let readyPrefix = "MARGINCE_READY"

final class AppDelegate: NSObject, NSApplicationDelegate, WKNavigationDelegate {
    private var window: NSWindow!
    private var webView: WKWebView!
    private var statusLabel: NSTextField!
    private let launcher = Process()

    func applicationDidFinishLaunching(_ notification: Notification) {
        buildWindow()
        startLauncher()
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        true
    }

    private func buildWindow() {
        window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 1280, height: 860),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false)
        window.title = "Margince"
        window.minSize = NSSize(width: 960, height: 600)
        window.center()

        webView = WKWebView(frame: window.contentView!.bounds)
        webView.autoresizingMask = [.width, .height]
        webView.navigationDelegate = self
        webView.isHidden = true

        // A first launch runs initdb and the full migration history, which is
        // seconds of nothing. Without a visible state the user sees a blank
        // window and concludes the app is broken.
        statusLabel = NSTextField(labelWithString: "Starting Margince…")
        statusLabel.alignment = .center
        statusLabel.font = .systemFont(ofSize: 15)
        statusLabel.textColor = .secondaryLabelColor
        statusLabel.frame = window.contentView!.bounds
        statusLabel.autoresizingMask = [.width, .height]

        window.contentView?.addSubview(webView)
        window.contentView?.addSubview(statusLabel)
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    private func startLauncher() {
        guard let resources = Bundle.main.resourceURL else {
            fail("This installation is damaged: its Resources folder is missing.")
            return
        }
        launcher.executableURL = resources.appendingPathComponent("margince-launcher")

        // The path is passed explicitly rather than left to the launcher's own
        // relative-path guess, so the two processes cannot disagree about
        // where the bundle is.
        var environment = ProcessInfo.processInfo.environment
        environment["MARGINCE_DESKTOP_RESOURCES"] = resources.path
        launcher.environment = environment

        let pipe = Pipe()
        launcher.standardOutput = pipe
        var buffer = Data()
        pipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
            buffer.append(handle.availableData)
            while let newline = buffer.firstIndex(of: 0x0A) {
                let line = String(decoding: buffer[buffer.startIndex..<newline], as: UTF8.self)
                buffer.removeSubrange(buffer.startIndex...newline)
                guard line.hasPrefix(readyPrefix) else { continue }
                let url = line.dropFirst(readyPrefix.count).trimmingCharacters(in: .whitespaces)
                pipe.fileHandleForReading.readabilityHandler = nil
                DispatchQueue.main.async { self?.load(url) }
                return
            }
        }

        launcher.terminationHandler = { [weak self] process in
            // The stack dying while the window is open is not something the
            // user can act on from the UI, so say so plainly and stay open
            // rather than vanishing.
            guard process.terminationStatus != 0 else { return }
            DispatchQueue.main.async {
                self?.fail("Margince stopped unexpectedly. See the logs in the data folder.")
            }
        }

        do {
            try launcher.run()
        } catch {
            fail("Margince could not start: \(error.localizedDescription)")
        }
    }

    private func load(_ url: String) {
        guard let target = URL(string: url) else {
            fail("Margince started but reported an unusable address: \(url)")
            return
        }
        webView.load(URLRequest(url: target))
        webView.isHidden = false
        statusLabel.isHidden = true
    }

    private func fail(_ message: String) {
        statusLabel.stringValue = message
        statusLabel.isHidden = false
        webView.isHidden = true
    }

    func applicationWillTerminate(_ notification: Notification) {
        guard launcher.isRunning else { return }
        // SIGINT is the launcher's documented quit signal; it stops the stack
        // in reverse order and shuts Postgres down cleanly. Waiting is the
        // point — exiting first would orphan a database mid-checkpoint.
        kill(launcher.processIdentifier, SIGINT)
        let deadline = Date().addingTimeInterval(45)
        while launcher.isRunning && Date() < deadline {
            RunLoop.current.run(mode: .default, before: Date().addingTimeInterval(0.1))
        }
        if launcher.isRunning {
            launcher.terminate()
        }
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.run()
