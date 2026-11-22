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
import { render, screen } from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import { api } from '@/lib/api'

import { AffiliateRewardsCard } from '../components/affiliate-rewards-card'
import { ReferralDetails } from '../components/referral-details'
import type { ReferralPermissions } from '../types'

const i18n = createInstance()

beforeEach(async () => {
  await i18n.use(initReactI18next).init({
    lng: 'en',
    fallbackLng: false,
    nsSeparator: false,
    resources: { en },
  })
})

afterEach(() => vi.restoreAllMocks())

it('shows earned invitation rewards with no transfer action', () => {
  render(
    <I18nextProvider i18n={i18n}>
      <AffiliateRewardsCard
        affiliateLink='https://example.com/sign-up?aff=abcd'
        earnedQuota={500000}
      />
    </I18nextProvider>
  )
  expect(screen.getByText('Total Earned')).toBeVisible()
  expect(
    screen.getByText(
      'Invitation rewards are added to your balance automatically.'
    )
  ).toBeVisible()
  expect(
    screen.getByRole('button', { name: 'Copy referral link' })
  ).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Transfer to Balance' })
  ).not.toBeInTheDocument()
})

function renderReferralDetails(permissions: ReferralPermissions) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <I18nextProvider i18n={i18n}>
        <ReferralDetails userId={72} permissions={permissions} />
      </I18nextProvider>
    </QueryClientProvider>
  )
}

it('shows masked email and pending rewards when invitation access is the only permission', async () => {
  const request = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    const items =
      url === '/api/user/referral/invitees'
        ? [{ id: 23, username: 'invitee', email: 'a***e@example.com' }]
        : [
            {
              id: 23,
              record_type: 'invitation_reward',
              username: 'invitee',
              paid_amount: '0',
              paid_currency: 'USD',
              rate_bps: 0,
              rebate_quota: 500_000,
              created_at: 1,
              credited_at: 0,
              remaining_seconds: 3600,
            },
          ]
    return { data: { success: true, data: { total: items.length, items } } }
  })
  renderReferralDetails({ invitation_enabled: true, rebate_enabled: false })
  expect(await screen.findByText('a***e@example.com')).toBeVisible()
  expect(
    await screen.findByRole('cell', { name: 'Invitation rewards' })
  ).toBeVisible()
  expect(screen.getByRole('cell', { name: '$1' })).toBeVisible()
  expect(screen.getByText('Estimated credit in 1h')).toBeVisible()
  expect(screen.queryByText(/Current rebate rate/)).not.toBeInTheDocument()
  expect(request.mock.calls.map(([url]) => url)).toEqual([
    '/api/user/referral/invitees',
    '/api/user/referral/rebates',
  ])
})

it('shows cancelled invitation rewards without an estimated credit time', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    const items =
      url === '/api/user/referral/invitees'
        ? []
        : [
            {
              id: 1,
              record_type: 'invitation_reward',
              username: '',
              rebate_quota: 500_000,
              created_at: 1,
              available_at: 3601,
              credited_at: 0,
              cancelled_at: 2,
            },
          ]
    return { data: { success: true, data: { total: items.length, items } } }
  })
  renderReferralDetails({ invitation_enabled: true, rebate_enabled: false })

  expect(await screen.findByRole('cell', { name: 'Cancelled' })).toBeVisible()
  expect(screen.queryByText(/Estimated credit|Pending/)).not.toBeInTheDocument()
})

it('shows the effective rate and paid currency with rebate access alone', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        total: 1,
        items: [
          {
            id: 1,
            username: 'invitee',
            paid_amount: '4.25',
            paid_currency: 'USD',
            rate_bps: 750,
            rebate_quota: 159375,
            created_at: 1,
            credited_at: 2,
          },
        ],
      },
    },
  })
  renderReferralDetails({
    invitation_enabled: false,
    rebate_enabled: true,
    rate_bps: 750,
  })
  expect(await screen.findByText(/USD\s4\.25/)).toBeVisible()
  expect(screen.getByText('Current rebate rate: 7.5%')).toBeVisible()
  expect(screen.getByText('Credited to wallet')).toBeVisible()
  expect(screen.queryByText('Invited Users')).not.toBeInTheDocument()
})

it('shows the redemption code face value as the rebate basis', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        total: 1,
        items: [
          {
            id: 1,
            username: 'invitee',
            paid_amount: '',
            paid_currency: '',
            redemption_quota: 4_000_000,
            rate_bps: 1000,
            rebate_quota: 400_000,
            created_at: 1,
            credited_at: 2,
          },
        ],
      },
    },
  })
  renderReferralDetails({ invitation_enabled: false, rebate_enabled: true })
  expect(
    await screen.findByRole('cell', { name: 'Redemption Code · $8' })
  ).toBeVisible()
})

it('shows the server countdown in hours and minutes and leaves due rebates pending', async () => {
  const rebate = {
    username: 'invitee',
    paid_amount: '4.25',
    paid_currency: 'USD',
    rate_bps: 750,
    rebate_quota: 159375,
    created_at: 1,
    available_at: 7201,
    credited_at: 0,
  }
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        total: 3,
        items: [
          { ...rebate, id: 1, remaining_seconds: 4500 },
          { ...rebate, id: 2, remaining_seconds: 30 },
          { ...rebate, id: 3, remaining_seconds: 0 },
        ],
      },
    },
  })
  renderReferralDetails({ invitation_enabled: false, rebate_enabled: true })
  expect(await screen.findByText('Estimated credit in 1h 15m')).toBeVisible()
  expect(screen.getByText('Estimated credit in 1m')).toBeVisible()
  expect(screen.getByText('Pending')).toBeVisible()
  expect(screen.queryByText('Credited to wallet')).not.toBeInTheDocument()
})

it('withholds both record requests and panels when both permissions are disabled', () => {
  const request = vi.spyOn(api, 'get')
  renderReferralDetails({ invitation_enabled: false, rebate_enabled: false })
  expect(screen.queryByRole('table')).not.toBeInTheDocument()
  expect(request).not.toHaveBeenCalled()
})
