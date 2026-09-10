// Independently authored MCP 2026-07-28 wire fixtures. No DSH naming or
// presentation contracts are imported. Regenerate with: node protocol.ts
import { writeFileSync } from 'node:fs';

type Elicitation = {
  method: 'elicitation/create';
  params: {
    mode: 'form' | 'url';
    message: string;
    requestedSchema?: Record<string, unknown>;
    elicitationId?: string;
    url?: string;
  };
};

type InputRequired = {
  resultType: 'input_required';
  requestState: string;
  inputRequests: Record<string, Elicitation>;
};

const inputRequired: InputRequired = {
  resultType: 'input_required',
  requestState: 'typescript-round-state',
  inputRequests: {
    formInput: {
      method: 'elicitation/create',
      params: {
        mode: 'form',
        message: 'Select a count',
        requestedSchema: {
          type: 'object',
          properties: { count: { type: 'integer' } },
          required: ['count'],
        },
      },
    },
    urlInput: {
      method: 'elicitation/create',
      params: {
        mode: 'url',
        message: 'Complete the external interaction',
        elicitationId: 'typescript-link',
        url: 'https://example.com/interaction',
      },
    },
  },
};

const fixtures = {
  discover: {
    resultType: 'complete',
    supportedVersions: ['2026-07-28'],
    capabilities: { tools: { listChanged: true } },
  },
  inputRequired,
  complete: {
    resultType: 'complete',
    content: [{ type: 'text', text: 'protocol fixture completed' }],
    structuredContent: { complete: true },
  },
};

writeFileSync(new URL('./protocol.json', import.meta.url), JSON.stringify(fixtures, null, 2) + '\n');
