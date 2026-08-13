# Goren

Goren explores a reusable Memory Agent for products that maintain an agent's static, long-lived memory. These products commonly focus on storing, organizing, and retrieving stable facts, preferences, instructions, or summaries, but they often lack an independent semantic component that continuously interprets how conversational knowledge changes.

Memory Agent combines an LLM-backed agent, controlled tools, and a two-stage workflow. It first extracts configurable knowledge objects from conversation context and determines whether the user supports, opposes, or is uncertain about each object, distinguishing explicit positions from contextual inference. It then retrieves related existing knowledge through a read-only tool and uses the LLM to merge or resolve conflicts into `add`, `update`, `keep`, or `delete` decisions, without depending on how knowledge is stored or managed.

The Chinese project background is available in [README.zh-CN.md](./README.zh-CN.md). Detailed design is maintained in the [Chinese design index](./zh-CN/README.md).
