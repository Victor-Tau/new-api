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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { SettingsSwitchField } from '@/features/system-settings/components/settings-form-layout'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'

import {
  getUserReferralPermissions,
  updateUserReferralPermissions,
} from '../../api'
import type { User, UserReferralPermissions } from '../../types'
import { useUsers } from '../users-provider'

type UserReferralDialogProps = {
  user: Pick<User, 'id' | 'username'>
  onClose: () => void
}

export function UserReferralDialog(props: UserReferralDialogProps) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['admin-user-referral', props.user.id],
    queryFn: () => getUserReferralPermissions(props.user.id),
  })
  if (query.isSuccess) {
    return <UserReferralForm {...props} permissions={query.data} />
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('Referral permissions')}
    >
      {query.isError ? (
        <p role='alert'>{t('Failed to load referral permissions')}</p>
      ) : (
        <Skeleton className='h-24 w-full' />
      )}
    </Dialog>
  )
}

function UserReferralForm(
  props: UserReferralDialogProps & { permissions: UserReferralPermissions }
) {
  const { t } = useTranslation()
  const { requestVerification, verificationActive } = useUsers()
  const queryClient = useQueryClient()
  const [invitationEnabled, setInvitationEnabled] = useState(
    props.permissions.invitation_enabled
  )
  const [rebateEnabled, setRebateEnabled] = useState(
    props.permissions.rebate_enabled
  )
  const [rate, setRate] = useState(
    props.permissions.rate_bps == null
      ? ''
      : String(props.permissions.rate_bps / 100)
  )

  const save = useMutation({
    mutationFn: async () => {
      const proof = await requestVerification({
        scope: 'admin.user.referral',
        context: { user_id: props.user.id },
        title: t('Referral permissions'),
        description: t(
          'Confirm your identity before changing the account {{username}}.',
          {
            username: props.user.username,
          }
        ),
      })
      if (!proof) return false
      await updateUserReferralPermissions(
        props.user.id,
        {
          invitation_enabled: invitationEnabled,
          rebate_enabled: rebateEnabled,
          rate_bps: rate === '' ? null : Math.round(Number(rate) * 100),
        },
        proof.proof_token
      )
      return true
    },
    onSuccess: (saved) => {
      if (!saved) return
      void queryClient.invalidateQueries({
        queryKey: ['admin-user-referral', props.user.id],
      })
      void queryClient.invalidateQueries({
        queryKey: ['referral-permissions', props.user.id],
      })
      toast.success(t('Update successful'))
      props.onClose()
    },
    onError: (error) =>
      handleServerError(AuthOperationError.from(error), t('Update failed')),
  })
  const formId = `user-referral-${props.user.id}`

  return (
    <Dialog
      open={!verificationActive}
      onOpenChange={(open) => {
        if (!open && !save.isPending) props.onClose()
      }}
      title={t('Referral permissions')}
      description={props.user.username}
      footer={
        <Button type='submit' form={formId} disabled={save.isPending}>
          {t('Save')}
        </Button>
      }
    >
      <form
        id={formId}
        onSubmit={(event) => {
          event.preventDefault()
          save.mutate()
        }}
      >
        <fieldset disabled={save.isPending} className='space-y-4'>
          <SettingsSwitchField
            label={t('Allow invitations')}
            checked={invitationEnabled}
            onCheckedChange={setInvitationEnabled}
          />
          <SettingsSwitchField
            label={t('Allow top-up rebates')}
            checked={rebateEnabled}
            onCheckedChange={setRebateEnabled}
          />
          <div className='grid gap-2'>
            <Label htmlFor={`${formId}-rate`}>
              {t('Rebate rate override (%)')}
            </Label>
            <Input
              id={`${formId}-rate`}
              type='number'
              min={0}
              max={100}
              step='0.01'
              value={rate}
              placeholder={t('Use site default')}
              onChange={(event) => setRate(event.target.value)}
            />
          </div>
        </fieldset>
      </form>
    </Dialog>
  )
}
