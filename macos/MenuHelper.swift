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

struct SetupInput {
    let server: String
    let models: String
    let error: String

    static func decode(_ data: Data) -> SetupInput? {
        guard data.count <= 16384,
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              Set(object.keys).isSubset(of: Set(["server", "models", "error"])),
              object.values.allSatisfy({ $0 is String }) else {
            return nil
        }
        return SetupInput(server: object["server"] as? String ?? "", models: object["models"] as? String ?? "", error: object["error"] as? String ?? "")
    }
}

func readSetupInput() -> SetupInput? {
    var data = Data()
    while data.count <= 16384 {
        do {
            guard let chunk = try FileHandle.standardInput.read(upToCount: 16385 - data.count), !chunk.isEmpty else {
                return SetupInput.decode(data)
            }
            data.append(chunk)
        } catch {
            return nil
        }
    }
    return nil
}

final class DialogDelegate: NSObject, NSApplicationDelegate {
    private let action: () -> Int32

    init(action: @escaping () -> Int32) {
        self.action = action
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        DispatchQueue.main.async {
            exit(self.action())
        }
    }
}

func runDialog(_ action: @escaping () -> Int32) -> Never {
    let application = NSApplication.shared
    let delegate = DialogDelegate(action: action)
    application.delegate = delegate
    application.setActivationPolicy(.accessory)
    withExtendedLifetime(delegate) {
        application.run()
    }
    exit(1)
}

func runSetup(_ input: SetupInput) -> Int32 {
    let application = NSApplication.shared
    application.setActivationPolicy(.accessory)
    let alert = NSAlert()
    alert.messageText = "DUMB Worker"
    alert.informativeText = input.error.isEmpty ? "Connect this Mac to your worker server." : input.error
    alert.addButton(withTitle: "Connect")
    alert.addButton(withTitle: "Cancel")
    let content = NSView(frame: NSRect(x: 0, y: 0, width: 380, height: 260))
    func label(_ title: String, _ y: CGFloat) {
        let field = NSTextField(labelWithString: title)
        field.frame = NSRect(x: 0, y: y, width: 380, height: 20)
        content.addSubview(field)
    }
    label("Server URL", 238)
    let server = NSTextField(string: input.server)
    server.placeholderString = "https://your-server.example"
    server.frame = NSRect(x: 0, y: 209, width: 380, height: 24)
    content.addSubview(server)
    label("Models (comma-separated)", 180)
    let models = NSTextField(string: input.models)
    models.frame = NSRect(x: 0, y: 151, width: 380, height: 24)
    content.addSubview(models)
    let authentication = NSPopUpButton(frame: NSRect(x: 0, y: 112, width: 380, height: 26), pullsDown: false)
    authentication.addItems(withTitles: ["Pairing code", "Worker token"])
    content.addSubview(authentication)
    label("Pairing code or worker token", 83)
    let credential = NSSecureTextField(string: "")
    credential.frame = NSRect(x: 0, y: 54, width: 380, height: 24)
    content.addSubview(credential)
    let login = NSButton(checkboxWithTitle: "Start at login", target: nil, action: nil)
    login.state = .on
    login.frame = NSRect(x: 0, y: 12, width: 380, height: 24)
    content.addSubview(login)
    alert.accessoryView = content
    alert.window.initialFirstResponder = server.stringValue.isEmpty ? server : credential
    application.activate(ignoringOtherApps: true)
    while alert.runModal() == .alertFirstButtonReturn {
        let serverValue = server.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
        let modelsValue = models.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
        let credentialValue = credential.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !serverValue.isEmpty, !modelsValue.isEmpty, !credentialValue.isEmpty else {
            alert.informativeText = "Enter a server URL, at least one model, and a pairing code or worker token."
            continue
        }
        let result: [String: Any] = [
            "server": serverValue,
            "models": modelsValue,
            "credential": credentialValue,
            "authentication": authentication.indexOfSelectedItem == 0 ? "pairing" : "token",
            "start_at_login": login.state == .on
        ]
        guard var data = try? JSONSerialization.data(withJSONObject: result, options: [.sortedKeys]) else {
            return 1
        }
        data.append(10)
        FileHandle.standardOutput.write(data)
        credential.stringValue = ""
        return 0
    }
    return 2
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

if CommandLine.arguments.count == 2 && CommandLine.arguments[1] == "--lifecycle-self-test" {
    runDialog {
        guard NSApplication.shared.isRunning, NSRunningApplication.current.isFinishedLaunching else {
            return 1
        }
        print("Native dialog lifecycle passed")
        return 0
    }
}

if CommandLine.arguments.count == 2 && CommandLine.arguments[1] == "--setup" {
    guard let input = readSetupInput() else {
        exit(1)
    }
    runDialog { runSetup(input) }
}

if CommandLine.arguments.count == 2 && CommandLine.arguments[1] == "--retry" {
    guard let input = readSetupInput() else {
        exit(1)
    }
    runDialog {
        let application = NSApplication.shared
        application.setActivationPolicy(.accessory)
        let alert = NSAlert()
        alert.messageText = "DUMB Worker"
        alert.informativeText = input.error.isEmpty ? "Setup could not finish. Retry to continue." : input.error
        alert.addButton(withTitle: "Retry")
        alert.addButton(withTitle: "Cancel")
        application.activate(ignoringOtherApps: true)
        let retry = alert.runModal() == .alertFirstButtonReturn
        guard var data = try? JSONSerialization.data(withJSONObject: ["retry": retry]) else {
            return 1
        }
        data.append(10)
        FileHandle.standardOutput.write(data)
        return 0
    }
}

if CommandLine.arguments.count == 2 && CommandLine.arguments[1] == "--message" {
    guard let input = readSetupInput() else {
        exit(1)
    }
    runDialog {
        let application = NSApplication.shared
        application.setActivationPolicy(.accessory)
        let alert = NSAlert()
        alert.messageText = "DUMB Worker"
        alert.informativeText = input.error.isEmpty ? "The worker could not start. Please try again." : input.error
        alert.addButton(withTitle: "OK")
        application.activate(ignoringOtherApps: true)
        alert.runModal()
        return 0
    }
}

if CommandLine.arguments.count == 2 && CommandLine.arguments[1] == "--setup-input-self-test" {
    guard readSetupInput() != nil else {
        exit(1)
    }
    print("Native setup input passed")
    exit(0)
}

if CommandLine.arguments.count == 2 && CommandLine.arguments[1] == "--setup-self-test" {
    let valid = Data(#"{"server":"https://example.invalid","models":"model","error":""}"#.utf8)
    guard let input = SetupInput.decode(valid), input.server == "https://example.invalid", input.models == "model", input.error.isEmpty else {
        exit(1)
    }
    for invalid in [Data("{".utf8), Data(#"{"server":true}"#.utf8), Data(#"{"credential":"rejected"}"#.utf8), Data(repeating: 32, count: 16385)] {
        if SetupInput.decode(invalid) != nil {
            exit(1)
        }
    }
    print("Native setup protocol self-test passed")
    exit(0)
}

let initialForeground = NSWorkspace.shared.frontmostApplication?.processIdentifier
let application = NSApplication.shared
let delegate = MenuDelegate()
application.delegate = delegate
application.setActivationPolicy(.accessory)
application.run()
