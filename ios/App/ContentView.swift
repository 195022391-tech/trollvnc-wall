import SwiftUI

struct ContentView: View {
    @AppStorage("wall.host.v3") private var host = "192.168.9.103"
    @AppStorage("wall.deviceId") private var deviceId = "iphone-1"
    @AppStorage("wall.token") private var token = "changeme"
    @AppStorage("wall.name") private var name = "WallPhone"
    @State private var logText = ""
    @State private var streaming = false
    private let session = RootStreamSession()

    var body: some View {
        NavigationView {
            Form {
                Section("电脑屏幕墙") {
                    TextField("主机 IP", text: $host)
                        .textInputAutocapitalization(.never)
                        .keyboardType(.asciiCapable)
                    TextField("deviceId（path）", text: $deviceId)
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
                Section("说明") {
                    Text("1. 墙页先登记相同 deviceId + token")
                    Text("2. RootEncoder → RTSP :8554/{deviceId}?token=")
                    Text("3. 编码 H.265，仅视频（相机）")
                    Text("v0.2 用 RootEncoder，非自研 RTSP")
                        .font(.footnote)
                        .foregroundColor(.secondary)
                }
                Section("日志") {
                    ScrollView {
                        Text(logText)
                            .font(.system(.footnote, design: .monospaced))
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .frame(minHeight: 180)
                }
            }
            .navigationTitle("屏幕墙推流")
        }
        .navigationViewStyle(.stack)
        .onAppear {
            session.onLog = { line in
                DispatchQueue.main.async {
                    let next = logText + "\n" + line
                    logText = next.count > 4000 ? String(next.suffix(4000)) : next
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
            logText = ""
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
