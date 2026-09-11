"""Generate image wire fixtures by executing the pinned Harness serializer."""
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
root = Path(sys.argv[1]).resolve()
revision = subprocess.check_output(['git', '-C', str(root), 'rev-parse', 'HEAD'], text=True).strip()
assert revision == '5dda764ed3aa172535a7967b06ff95d9cbfe536a'
def source(path):
    return re.sub(r'^import\b[\s\S]*?from [^\n]+\n', '', (root / path).read_text(), flags=re.M)
program = 'class LlmError extends Error {}\nfunction assertNever(value) { throw new Error(String(value)) }\n'
program += source('packages/llm/llm/src/content.ts')
program += source('packages/llm/llm-deepseek/src/serialize.ts')
program += '''
const ref = { attachmentId: 'sha256:picture', mediaType: 'image/png', width: 2, height: 3, bytes: 1, name: 'picture.png' }
const picture = { type: 'image', attachment: ref }
const text = value => ({type:'text',text:value})
const cases = [
 {name:'mixed', messages:[{role:'user',content:[text('before'),picture,text('after')]}]},
 {name:'only-image', messages:[{role:'user',content:[picture]}]},
 {name:'tool-images', messages:[{role:'user',content:[{type:'tool-result',toolCallId:'one',content:[text('found'),picture]}]},{role:'user',content:[{type:'tool-result',toolCallId:'two',content:[picture]}]}]},
 {name:'text', messages:[{role:'user',content:[text('hello')]}]},
]
for (const sample of cases) sample.wire = await serializeMessagesWithImages(sample.messages, {
 representation: {kind:'base64'}, requestImages:new Map([[ref.attachmentId,{...ref,data:new Uint8Array([1])}]]), maxRequestImageBytes:40*1024*1024
})
console.log(JSON.stringify(cases,null,2))
'''
with tempfile.TemporaryDirectory() as directory:
    path = Path(directory) / 'fixture.mts'
    path.write_text(program)
    output = subprocess.check_output(['node','--experimental-strip-types',str(path)],text=True)
Path(__file__).with_name('images.json').write_text(json.dumps({'revision':revision,'cases':json.loads(output)},indent=2)+'\n')
