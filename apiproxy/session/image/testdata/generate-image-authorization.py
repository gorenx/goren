"""Execute unchanged pinned Harness authorization helpers with Node type stripping.
Usage: python3 generate-image-authorization.py /path/to/deepseek-harness
Only type-only dependencies are omitted; runtime helper bodies are copied verbatim.
"""
import json
from pathlib import Path
import subprocess
import sys
import tempfile

checkout = Path(sys.argv[1]).resolve()
revision = subprocess.check_output(['git', '-C', str(checkout), 'rev-parse', 'HEAD'], text=True).strip()
expected = '5dda764ed3aa172535a7967b06ff95d9cbfe536a'
if revision != expected:
    raise SystemExit('Harness revision differs from image baseline')
commands = (checkout / 'packages/api/session-controller/src/commands.ts').read_text()
stream = (checkout / 'packages/llm/llm/src/assistant-stream.ts').read_text()

def body(source, name):
    start = source.index('function ' + name)
    end = source.index('\n}', start) + 2
    return source[start:end]

program = '\n\n'.join([
    body(stream, 'assistantStreamChunks'),
    body(commands, 'imageBlockIn'),
    body(commands, 'imageInEvent'),
    body(commands, 'referencedImage'),
])
program += '''
const ref = { attachmentId: 'sha256:' + 'a'.repeat(64), mediaType: 'image/png', bytes: 1, width: 1, height: 1, name: 'sample.png' }
const block = { type: 'image', attachment: ref }
const chunk = { type: 'chunk', time: 1, chunk: { type: 'block-end', index: 0, block } }
const cases = [
 ['direct', 'fixture/direct', { content: [block] }],
 ['unrelated-stream-shape', 'fixture/direct', { content: [block], stream: 'extension data' }],
 ['nested', 'tool/result', { content: [null, [], { type: 'tool-result', content: [{ type: 'tool-result', content: [block] }] }] }],
 ['message', 'assistant/message', { message: { content: [block] }, stream: [] }],
 ['inserted', 'agent/inbox/spliced', { inserted: [{ content: [block] }] }],
 ['attempt', 'assistant/attempt', { stream: [chunk] }],
 ['settlement', 'assistant/message', { stream: [chunk] }],
 ['unrelated-stream', 'fixture/unrelated', { stream: [chunk] }],
 ['non-tool-nesting', 'fixture/unrelated', { content: [{ type: 'text', content: [block] }] }],
 ['unrelated-field', 'fixture/unrelated', { other: block }],
 ['incomplete-block', 'assistant/attempt', { stream: [{ ...chunk, chunk: { ...chunk.chunk, type: 'block-start' } }] }],
 ['unrelated-record', 'assistant/attempt', { stream: [{ ...chunk, type: 'other' }] }],
 ['different-image', 'fixture/direct', { content: [{ type: 'image', attachment: { ...ref, attachmentId: 'different' } }] }],
].map(([name, type, data]) => {
 const events = [{ type, seq: 0, time: 1, data }]
 const found = referencedImage(events, ref.attachmentId)
 return { name, events, request: { sessionId: 'conversation', attachmentId: ref.attachmentId }, value: found ? { attachment: found, data: Buffer.from([1]).toString('base64') } : null }
})
console.log(JSON.stringify({ cases }, null, 2))
'''
with tempfile.TemporaryDirectory() as directory:
    script = Path(directory) / 'authorization.ts'
    script.write_text(program)
    output = subprocess.check_output(['node', '--experimental-strip-types', str(script)], text=True)
fixture = json.loads(output)
fixture['source'] = {'revision': revision, 'symbols': ['commands.ts:referencedImage', 'commands.ts:imageInEvent', 'commands.ts:imageBlockIn', 'assistant-stream.ts:assistantStreamChunks']}
Path(__file__).with_name('image-authorization.json').write_text(json.dumps(fixture, indent=2) + '\n')
