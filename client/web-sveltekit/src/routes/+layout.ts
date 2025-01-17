import { error } from '@sveltejs/kit'

import { browser } from '$app/environment'
import { isErrorLike, parseJSONCOrError } from '$lib/common'
import { getGraphQLClient } from '$lib/graphql'
import type { Settings } from '$lib/shared'

import type { LayoutLoad } from './$types'
import { Init, EvaluatedFeatureFlagsQuery } from './layout.gql'

// Disable server side rendering for the whole app
export const ssr = false
export const prerender = false

if (browser) {
    // Necessary to make authenticated GrqphQL requests work
    // No idea why TS picks up Mocha.SuiteFunction for this
    // @ts-ignore
    window.context = {
        xhrHeaders: {
            'X-Requested-With': 'Sourcegraph',
        },
    }
}

export const load: LayoutLoad = async ({ fetch }) => {
    const client = getGraphQLClient()
    const result = await client.query(Init, {}, { fetch, requestPolicy: 'network-only' })
    if (!result.data || result.error) {
        error(500, `Failed to initialize app: ${result.error}`)
    }

    const settings = parseJSONCOrError<Settings>(result.data.viewerSettings.final)
    if (isErrorLike(settings)) {
        error(500, `Failed to parse user settings: ${settings.message}`)
    }

    return {
        user: result.data.currentUser,
        // Initial user settings
        settings,
        featureFlags: result.data.evaluatedFeatureFlags,
        fetchEvaluatedFeatureFlags: async () => {
            const result = await client.query(EvaluatedFeatureFlagsQuery, {}, { requestPolicy: 'network-only', fetch })
            if (!result.data || result.error) {
                throw new Error(`Failed to fetch evaluated feature flags: ${result.error}`)
            }
            return result.data.evaluatedFeatureFlags
        },
    }
}
