/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useState } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { ReferralSettingsSection } from '../referral-settings-section'

function Fixture() {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <ReferralSettingsSection
          defaultRateBps={750}
          exchangeRates='{"EUR":0.92}'
          inviterQuota={500000}
          inviteeQuota={100000}
          invitationDelayHours={0}
          rebateDelayHours={0}
          complianceConfirmed
        />
      </SettingsPageProvider>
    </>
  )
}

afterEach(() => vi.restoreAllMocks())

it('saves visual exchange rates and independent delays in the same request', async () => {
  const save = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const client = new QueryClient()
  const router = createRouter({
    routeTree: createRootRoute({ component: Fixture }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )

  const rate = await screen.findByPlaceholderText('0.92')
  expect(rate).toHaveValue('0.92')
  fireEvent.change(rate, { target: { value: '0.95' } })
  fireEvent.click(screen.getByRole('button', { name: 'JSON Mode' }))
  expect(screen.getByRole('button', { name: 'Visual Mode' })).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Visual Mode' }))
  expect(screen.getByPlaceholderText('0.92')).toHaveValue('0.95')
  fireEvent.change(
    screen.getByRole('spinbutton', { name: 'Inviter reward delay (hours)' }),
    {
      target: { value: '6' },
    }
  )
  fireEvent.change(
    screen.getByRole('spinbutton', { name: 'Top-up rebate delay (hours)' }),
    {
      target: { value: '24' },
    }
  )
  fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))

  await waitFor(() =>
    expect(save).toHaveBeenCalledWith('/api/option/referral', {
      rate_bps: 750,
      exchange_rates: JSON.stringify({ EUR: 0.95 }, null, 2),
      inviter_quota: 500000,
      invitee_quota: 100000,
      invitation_delay_hours: 6,
      rebate_delay_hours: 24,
    })
  )
})
