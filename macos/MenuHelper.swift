import AppKit
import CoreFoundation
import Darwin
import Foundation

struct WorkerStatus {
    let state: String
    let active: Int
    let category: String

    static func decode(_ data: Data) -> WorkerStatus? {
        guard let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              Set(object.keys) == Set(["version", "state", "active", "category"]),
              let version = object["version"] as? NSNumber,
              CFGetTypeID(version) != CFBooleanGetTypeID(),
              version.intValue == 1,
              version.doubleValue == 1,
              let state = object["state"] as? String,
              ["starting", "ready", "serving", "recovering", "error", "draining", "paused", "updating", "stopping"].contains(state),
              let active = object["active"] as? NSNumber,
              CFGetTypeID(active) != CFBooleanGetTypeID(),
              active.doubleValue >= 0,
              active.doubleValue < Double(Int.max),
              active.doubleValue.rounded(.towardZero) == active.doubleValue,
              let category = object["category"] as? String,
              ["none", "backend", "credentials", "upgrade", "ui"].contains(category) else {
            return nil
        }
        return WorkerStatus(state: state, active: active.intValue, category: category)
    }
}

final class MenuDelegate: NSObject, NSApplicationDelegate {
    private var item: NSStatusItem?
    private let statusItem = NSMenuItem(title: "Starting", action: nil, keyEquivalent: "")
    private let activeItem = NSMenuItem(title: "Active jobs: 0", action: nil, keyEquivalent: "")
    private let controlItem = NSMenuItem(title: "Pause", action: #selector(toggle), keyEquivalent: "")
    private var paused = false

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApplication.shared.setActivationPolicy(.accessory)
        let status = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        item = status
        status.button?.image = icon(for: "starting")
        status.button?.toolTip = "DUMB Worker"
        let menu = NSMenu()
        menu.autoenablesItems = false
        statusItem.isEnabled = false
        activeItem.isEnabled = false
        controlItem.target = self
        menu.addItem(statusItem)
        menu.addItem(activeItem)
        menu.addItem(.separator())
        menu.addItem(controlItem)
        let quit = NSMenuItem(title: "Quit", action: #selector(quitWorker), keyEquivalent: "")
        quit.target = self
        menu.addItem(quit)
        status.menu = menu
        emit("ready")
        DispatchQueue.global(qos: .utility).async { [weak self] in
            var pending = Data()
            while true {
                let chunk = FileHandle.standardInput.availableData
                if chunk.isEmpty {
                    DispatchQueue.main.async { self?.finish() }
                    return
                }
                pending.append(chunk)
                while let newline = pending.firstIndex(of: 10) {
                    let line = Data(pending[..<newline])
                    pending.removeSubrange(...newline)
                    guard line.count <= 4096, let update = WorkerStatus.decode(line) else {
                        DispatchQueue.main.async { self?.finish() }
                        return
                    }
                    DispatchQueue.main.async { self?.apply(update) }
                }
                if pending.count > 4096 {
                    DispatchQueue.main.async { self?.finish() }
                    return
                }
            }
        }
    }

    private func apply(_ update: WorkerStatus) {
        paused = update.state == "paused"
        let category = update.category == "none" ? "" : " (\(update.category))"
        statusItem.title = update.state.capitalized + category
        activeItem.title = "Active jobs: \(update.active)"
        controlItem.title = paused ? "Resume" : "Pause"
        controlItem.isEnabled = !["draining", "updating", "stopping", "error"].contains(update.state)
        item?.button?.image = icon(for: update.state)
        item?.button?.appearsDisabled = ["paused", "draining", "stopping"].contains(update.state)
        diagnostics(update)
    }

    private func icon(for state: String) -> NSImage? {
        // The bear is a template image so it follows the light/dark menu bar;
        // errors keep a distinct symbol, and missing resources fall back to symbols.
        let description = "DUMB Worker \(state)"
        var image: NSImage?
        if state != "error", let bear = Bundle.main.image(forResource: "menubar") {
            bear.size = NSSize(width: 18, height: 18)
            bear.accessibilityDescription = description
            image = bear
        } else {
            let symbol = state == "error" ? "exclamationmark.circle" : "circle.dotted"
            image = NSImage(systemSymbolName: symbol, accessibilityDescription: description)
        }
        image?.isTemplate = true
        return image
    }

    private func diagnostics(_ update: WorkerStatus) {
        guard let path = ProcessInfo.processInfo.environment["QA_MENU_DIAGNOSTICS"], !path.isEmpty else {
            return
        }
        let policy: String
        switch NSApplication.shared.activationPolicy() {
        case .accessory: policy = "accessory"
        case .regular: policy = "regular"
        case .prohibited: policy = "prohibited"
        @unknown default: policy = "unknown"
        }
        let foreground = NSWorkspace.shared.frontmostApplication?.processIdentifier
        let windowClasses = NSApplication.shared.windows.map { NSStringFromClass(type(of: $0)) }
        let frameworkSurfaces = Set(["NSStatusBarWindow", "NSMenuWindow", "_NSMenuWindow"])
        let record: [String: Any] = [
            "state": update.state,
            "active": update.active,
            "applicationWindows": NSApplication.shared.windows.count,
            "statusWindowNumber": item?.button?.window?.windowNumber ?? -1,
            "windowClasses": windowClasses,
            "contentWindowCount": windowClasses.filter { !frameworkSurfaces.contains($0) }.count,
            "activationPolicy": policy,
            "isActive": NSApplication.shared.isActive,
            "focusUnchanged": initialForeground != nil && initialForeground == foreground
        ]
        guard var data = try? JSONSerialization.data(withJSONObject: record, options: [.sortedKeys]) else {
            return
        }
        data.append(10)
        let descriptor = Darwin.open(path, O_WRONLY | O_APPEND | O_CREAT | O_NOFOLLOW, 0o600)
        guard descriptor >= 0 else {
            return
        }
        data.withUnsafeBytes { buffer in
            _ = Darwin.write(descriptor, buffer.baseAddress, buffer.count)
        }
        Darwin.close(descriptor)
    }

    private func emit(_ event: String) {
        guard var data = try? JSONSerialization.data(withJSONObject: ["version": 1, "event": event], options: [.sortedKeys]) else {
            finish()
            return
        }
        data.append(10)
        FileHandle.standardOutput.write(data)
    }

    @objc private func toggle() {
        controlItem.isEnabled = false
        emit(paused ? "resume" : "pause")
    }

    @objc private func quitWorker() {
        controlItem.isEnabled = false
        emit("quit")
    }

    private func finish() {
        if let item {
            NSStatusBar.system.removeStatusItem(item)
        }
        NSApplication.shared.terminate(nil)
    }
}

if CommandLine.arguments.count == 2 && CommandLine.arguments[1] == "--self-test" {
    let states = ["starting", "ready", "serving", "recovering", "error", "draining", "paused", "updating", "stopping"]
    let categories = ["none", "backend", "credentials", "upgrade", "ui"]
    for state in states {
        for category in categories {
            let data = try! JSONSerialization.data(withJSONObject: ["version": 1, "state": state, "active": 2, "category": category])
            guard let status = WorkerStatus.decode(data), status.state == state, status.category == category, status.active == 2 else {
                exit(1)
            }
        }
    }
    let invalid = [
        "{}",
        "{",
        #"{"version":true,"state":"ready","active":0,"category":"none"}"#,
        #"{"version":2,"state":"ready","active":0,"category":"none"}"#,
        #"{"version":1,"state":"unknown","active":0,"category":"none"}"#,
        #"{"version":1,"state":"ready","active":true,"category":"none"}"#,
        #"{"version":1,"state":"ready","active":-1,"category":"none"}"#,
        #"{"version":1,"state":"ready","active":0.5,"category":"none"}"#,
        #"{"version":1,"state":"ready","active":0,"category":"unknown"}"#,
        #"{"version":1,"state":"ready","active":0,"category":"none","extra":"rejected"}"#
    ]
    for line in invalid {
        if WorkerStatus.decode(Data(line.utf8)) != nil {
            exit(1)
        }
    }
    print("Native menu protocol self-test passed")
    exit(0)
}

let initialForeground = NSWorkspace.shared.frontmostApplication?.processIdentifier
let application = NSApplication.shared
let delegate = MenuDelegate()
application.delegate = delegate
application.setActivationPolicy(.accessory)
application.run()
