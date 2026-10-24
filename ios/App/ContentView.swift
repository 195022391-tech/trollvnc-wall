import SwiftUI

struct ContentView: View {
    @AppStorage("wall.host.v3") private var host = "192.168.9.103"
    @AppStorage("wall.deviceId") private var deviceId = "iphone-1"
    @AppStorage("wall.token") private var token = "changeme"
    @AppStorage("wall.name") private var name = "WallPhone"
    @State private var logText = "（点开始后这里会出日志）"
    @State private var streaming = false
    private let session = RootStreamSession()

    var body: some View {
        NavigationView {
            Form {
                Section("电脑屏幕墙") {
                    TextField("主机 IP", text: $host)
                        .textInputAutocapitalization(.never)
                        .keyboardType(.asciiCapable)
                    TextField("deviceId", text: $deviceId)
                        .textInputAutocapitalization(.never)
                    SecureField("token", text: $token)
                    TextField("显示名", text: $name)
                }
                Section {
                    Button(streaming ? "停止推流" : "开始推流（相机 H.265）") {
                        toggle()
                    }
                    .foregroundColor(streaming ? .red : .accentColor)
                }
                Section("运行日志") {
                    Text(logText)
                        .font(.system(.footnote, design: .monospaced))
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .textSelection(.enabled)
                }
                Section("说明") {
                    Text("登记相同 deviceId+token → 填电脑 IP → 允许相机 → 开始")
                        .font(.footnote)
                        .foregroundColor(.secondary)
                }
            }
            .navigationTitle("屏幕墙推流")
        }
        .navigationViewStyle(.stack)
        .onAppear {
            session.onLog = { line in
                DispatchQueue.main.async {
                    if logText.hasPrefix("（点开始") { logText = line }
                    else {
                        let next = logText + "\n" + line
                        logText = next.count > 4000 ? String(next.suffix(4000)) : next
                    }
                }
            }
        }
    }

    private func toggle() {
        if streaming {
            session.stop()
            streaming = false
            append("stopped")
        } else {
            logText = "starting…"
            session.start(
                host: host.trimmingCharacters(in: .whitespaces),
                deviceId: deviceId.trimmingCharacters(in: .whitespaces),
                token: token,
                name: name
            )
            streaming = true
        }
    }

    private func append(_ s: String) {
        logText += "\n" + s
    }
}
