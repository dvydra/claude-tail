// entire-tail-aisum — on-device session summarizer using Apple Intelligence
// (the Foundation Models framework). Reads a plain-text transcript on stdin and
// prints a structured summary as JSON on stdout. Compiled by install.sh when the
// toolchain + framework are present (macOS 26+/Apple Silicon); entire-tail's `i`
// summary card runs it when available. Exit 2 = model unavailable, 1 = error.

import Foundation
import FoundationModels

@Generable
struct SessionSummary: Codable {
    @Guide(description: "A 6-10 word title for the session")
    var headline: String
    @Guide(description: "2-3 sentence summary: what the user wanted and what happened")
    var summary: String
    @Guide(description: "3 to 5 short bullet points of the key actions or decisions")
    var keyPoints: [String]
    @Guide(description: "One sentence on the final outcome or status")
    var outcome: String
}

let input = String(data: FileHandle.standardInput.readDataToEndOfFile(), encoding: .utf8) ?? ""

switch SystemLanguageModel.default.availability {
case .available:
    break
case .unavailable(let reason):
    FileHandle.standardError.write("apple-intelligence unavailable: \(reason)\n".data(using: .utf8)!)
    exit(2)
}

let sem = DispatchSemaphore(value: 0)
Task {
    do {
        let session = LanguageModelSession(
            instructions: "You summarize AI coding-agent session transcripts accurately and concisely.")
        // The on-device model has a small context window, so summarize the tail.
        let reply = try await session.respond(
            to: "Summarize this coding-agent session transcript:\n\n" + String(input.suffix(6000)),
            generating: SessionSummary.self)
        FileHandle.standardOutput.write(try JSONEncoder().encode(reply.content))
    } catch {
        FileHandle.standardError.write("error: \(error)\n".data(using: .utf8)!)
        exit(1)
    }
    sem.signal()
}
sem.wait()
