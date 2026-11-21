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
import { useQuery } from '@tanstack/react-query'

import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { getAffiliateCode, getReferralPermissions } from '../api'
import { generateAffiliateLink } from '../lib'

// ============================================================================
// Affiliate Hook
// ============================================================================

export function useAffiliate() {
  const userId = useAuthStore((state) => state.auth.user?.id)
  const permissions = useQuery({
    queryKey: ['referral-permissions', userId],
    queryFn: getReferralPermissions,
    enabled: userId !== undefined,
  })
  const invitationEnabled =
    permissions.isSuccess && permissions.data.invitation_enabled
  const code = useQuery({
    queryKey: ['referral-code', userId],
    enabled: invitationEnabled,
    queryFn: async () => requireServerSuccess(await getAffiliateCode()).data,
  })
  const affiliateLink =
    invitationEnabled && code.data ? generateAffiliateLink(code.data) : ''

  return {
    affiliateLink,
    invitationEnabled,
    permissions: permissions.isSuccess ? permissions.data : undefined,
    permissionsError: permissions.isError,
    loading: invitationEnabled && code.isPending,
    userId,
  }
}
